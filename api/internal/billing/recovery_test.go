package billing_test

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing/stripetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var missingCustomer = stripetest.Failure{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "resource_missing", Param: "customer"}

func TestCheckoutReplacesADeletedOrOtherModeCustomer(t *testing.T) {
	for name, prepare := range map[string]func(h harness, user store.User) string{
		"deleted in the dashboard": func(h harness, user store.User) string {
			h.stripe.AddCustomer("cus_deleted", user.ID.String())
			h.stripe.DeleteCustomer("cus_deleted")
			return "cus_deleted"
		},
		"stored in the other mode": func(_ harness, _ store.User) string {
			return "cus_from_live_mode"
		},
	} {
		h := newHarness(t)
		user := testUser("ada@example.com")
		dead := prepare(h, user)
		h.store.customers[user.ID] = dead

		link, err := h.svc.StartCheckout(t.Context(), user)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if link == "" {
			t.Errorf("%s: no url", name)
		}
		stored := h.store.customers[user.ID]
		if stored == "" || stored == dead || h.stripe.Customers()[stored] != user.ID.String() {
			t.Errorf("%s: stored %q, fake %v", name, stored, h.stripe.Customers())
		}
		creates := h.stripe.RequestsTo("/v1/customers")
		if len(creates) != 1 || creates[0].IdempotencyKey != billing.CustomerIdempotencyKey(user, dead) || creates[0].IdempotencyKey == billing.CustomerIdempotencyKey(user, "") {
			t.Errorf("%s: customer creates %+v", name, creates)
		}
		sessions := h.stripe.RequestsTo("/v1/checkout/sessions")
		if len(sessions) != 1 || sessions[0].Form.Get("customer") != stored {
			t.Errorf("%s: sessions %+v", name, sessions)
		}
		logs := h.logs.String()
		if h.store.clears != 1 || !strings.Contains(logs, "the stored stripe customer no longer exists") || !strings.Contains(logs, dead) {
			t.Errorf("%s: clears %d, logs %s", name, h.store.clears, logs)
		}
	}
}

func TestCheckoutReplacesTheCustomerOnlyOnce(t *testing.T) {
	h := newHarness(t)
	user := testUser("")
	h.store.customers[user.ID] = "cus_gone"
	h.stripe.FailNext("/v1/checkout/sessions", missingCustomer)

	if _, err := h.svc.StartCheckout(t.Context(), user); !errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if n := len(h.stripe.RequestsTo("/v1/customers")); n != 1 {
		t.Errorf("%d customers created, want 1", n)
	}
	if n := len(h.stripe.RequestsTo("/v1/checkout/sessions")); n != 1 {
		t.Errorf("%d session attempts, want 1", n)
	}
}

func TestOtherMissingResourcesAreNotTreatedAsADeadCustomer(t *testing.T) {
	h := newHarness(t)
	user := testUser("")
	h.store.customers[user.ID] = "cus_known"
	h.stripe.AddCustomer("cus_known", "")
	h.stripe.FailNext("/v1/checkout/sessions", stripetest.Failure{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "resource_missing", Param: "line_items[0][price]"})

	if _, err := h.svc.StartCheckout(t.Context(), user); !errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if h.store.customers[user.ID] != "cus_known" || h.store.clears != 0 {
		t.Error("the customer was cleared for a missing price")
	}
}

func TestReplacingFailsCleanlyWhenTheStoreFails(t *testing.T) {
	boom := errors.New("database down")
	h := newHarness(t)
	user := testUser("")
	h.store.customers[user.ID] = "cus_gone"
	h.store.clearErr = boom

	if _, err := h.svc.StartCheckout(t.Context(), user); !errors.Is(err, boom) {
		t.Errorf("checkout err = %v, want the store error", err)
	}
	if _, err := h.svc.OpenPortal(t.Context(), user); !errors.Is(err, boom) {
		t.Errorf("portal err = %v, want the store error", err)
	}
	if n := len(h.stripe.RequestsTo("/v1/customers")); n != 0 {
		t.Errorf("created %d customers", n)
	}
}

func TestPortalForADeletedCustomerClearsItAndAsksForCheckout(t *testing.T) {
	h := newHarness(t)
	user := testUser("")
	h.store.customers[user.ID] = "cus_deleted"

	if _, err := h.svc.OpenPortal(t.Context(), user); !errors.Is(err, billing.ErrNoCustomer) {
		t.Fatalf("err = %v, want ErrNoCustomer", err)
	}
	if got, ok := h.store.customers[user.ID]; ok {
		t.Errorf("stored customer = %q, want cleared", got)
	}
	if n := len(h.stripe.RequestsTo("/v1/customers")); n != 0 {
		t.Errorf("the portal created %d customers", n)
	}
	if _, err := h.svc.OpenPortal(t.Context(), user); !errors.Is(err, billing.ErrNoCustomer) {
		t.Errorf("second portal err = %v", err)
	}
	if n := len(h.stripe.RequestsTo("/v1/billing_portal/sessions")); n != 1 {
		t.Errorf("%d portal calls, want 1 (the second needs none)", n)
	}
	if _, err := h.svc.StartCheckout(t.Context(), user); err != nil {
		t.Errorf("checkout after the portal cleared the customer: %v", err)
	}
}

func TestAChangedEmailAfterAFailedStoreIsNotAnIdempotencyError(t *testing.T) {
	user := testUser("old@example.com")

	h := newHarness(t)
	h.store.setErr = errors.New("database down")
	if _, err := h.svc.StartCheckout(t.Context(), user); err == nil {
		t.Fatal("first checkout succeeded with a failing store")
	}
	h.store.setErr = nil
	if _, err := h.svc.StartCheckout(t.Context(), user); err != nil {
		t.Fatalf("retry with the same email: %v", err)
	}
	if n := len(h.stripe.Customers()); n != 1 {
		t.Errorf("a retry with the same email made %d customers, want the same one", n)
	}

	h = newHarness(t)
	h.store.setErr = errors.New("database down")
	if _, err := h.svc.StartCheckout(t.Context(), user); err == nil {
		t.Fatal("first checkout succeeded with a failing store")
	}
	h.store.setErr = nil
	email := "new@example.com"
	changed := user
	changed.Email = &email
	if _, err := h.svc.StartCheckout(t.Context(), changed); err != nil {
		t.Fatalf("retry with a new email: %v", err)
	}
	creates := h.stripe.RequestsTo("/v1/customers")
	if len(creates) != 2 || creates[0].IdempotencyKey == creates[1].IdempotencyKey || creates[1].Form.Get("email") != email {
		t.Errorf("creates %+v, want two keys and the new email", creates)
	}
	if stored := h.store.customers[user.ID]; h.stripe.Customers()[stored] == "" {
		t.Errorf("stored %q is not a Stripe customer", stored)
	}
}

func TestCustomerKeysDependOnTheParametersAndTheDeadCustomer(t *testing.T) {
	user := testUser("old@example.com")
	email := "new@example.com"
	changed := user
	changed.Email = &email
	if billing.CustomerIdempotencyKey(user, "") == billing.CustomerIdempotencyKey(changed, "") {
		t.Error("the key does not depend on the email")
	}
	again := user
	if billing.CustomerIdempotencyKey(user, "") != billing.CustomerIdempotencyKey(again, "") {
		t.Error("the key is not stable")
	}
	if billing.CustomerIdempotencyKey(user, "cus_a") == billing.CustomerIdempotencyKey(user, "cus_b") || billing.CustomerIdempotencyKey(user, "cus_a") == billing.CustomerIdempotencyKey(user, "") {
		t.Error("the replacement key does not depend on the dead customer")
	}
	if n := len(billing.CustomerIdempotencyKey(user, strings.Repeat("x", 300))); n > 255 {
		t.Errorf("key is %d characters, over Stripe's 255", n)
	}
}

func TestConcurrentRecoveryKeepsOneNewCustomer(t *testing.T) {
	h := newHarness(t)
	user := testUser("ada@example.com")
	h.store.customers[user.ID] = "cus_deleted"
	const n = 6
	h.stripe.OnCustomerCreate(stripetest.Barrier(n))

	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, err := h.svc.StartCheckout(t.Context(), user); err != nil {
				t.Errorf("StartCheckout: %v", err)
			}
		})
	}
	wg.Wait()

	stored := h.store.customers[user.ID]
	if stored == "" || stored == "cus_deleted" || h.stripe.Customers()[stored] == "" {
		t.Errorf("stored %q, fake customers %v", stored, h.stripe.Customers())
	}
	replacement, normal := billing.CustomerIdempotencyKey(user, "cus_deleted"), billing.CustomerIdempotencyKey(user, "")
	keys := map[string]bool{}
	for _, r := range h.stripe.RequestsTo("/v1/customers") {
		keys[r.IdempotencyKey] = true
		if r.IdempotencyKey != replacement && r.IdempotencyKey != normal {
			t.Errorf("customer key %q", r.IdempotencyKey)
		}
	}
	if len(h.stripe.Customers()) != len(keys) {
		t.Errorf("%d customers for %d keys", len(h.stripe.Customers()), len(keys))
	}
	for _, r := range h.stripe.RequestsTo("/v1/checkout/sessions") {
		if r.Form.Get("customer") != stored {
			t.Errorf("a session used %q, stored %q", r.Form.Get("customer"), stored)
		}
	}
}

func TestAConcurrentInFlightKeyIsAConflictThatNeverMakesASecondCustomer(t *testing.T) {
	h := newHarness(t)
	h.stripe.RejectConcurrentKeys()
	release := h.stripe.HoldCustomerCreates()
	t.Cleanup(release)
	user := testUser("")

	first := make(chan error, 1)
	go func() {
		_, err := h.svc.StartCheckout(t.Context(), user)
		first <- err
	}()
	for len(h.stripe.RequestsTo("/v1/customers")) == 0 {
		time.Sleep(time.Millisecond)
	}

	if _, err := h.svc.StartCheckout(t.Context(), user); !errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("second err = %v, want ErrUnavailable while the first is in flight", err)
	}
	if n := len(h.stripe.RequestsTo("/v1/customers")); n != 3 {
		t.Errorf("%d customer requests, want 3 (the first, then a 409 and its one retry)", n)
	}
	release()
	if err := <-first; err != nil {
		t.Fatalf("first: %v", err)
	}
	if n := len(h.stripe.Customers()); n != 1 {
		t.Errorf("%d customers, want 1", n)
	}
	if _, err := h.svc.StartCheckout(t.Context(), user); err != nil {
		t.Errorf("a later checkout: %v", err)
	}
}

func TestReturnedURLsMustBeHTTPS(t *testing.T) {
	for _, link := range []string{"http://checkout.stripe.test/c/pay/cs_1", "https://user:pass@checkout.stripe.test/c/pay/cs_1", "not a url", "https://", "::", ""} {
		h := newHarness(t)
		h.stripe.ReturnSessionURL(link)
		user := testUser("")
		if _, err := h.svc.StartCheckout(t.Context(), user); !errors.Is(err, billing.ErrUnavailable) {
			t.Errorf("checkout %q: err = %v", link, err)
		}
		if _, err := h.svc.OpenPortal(t.Context(), user); !errors.Is(err, billing.ErrUnavailable) {
			t.Errorf("portal %q: err = %v", link, err)
		}
	}
}
