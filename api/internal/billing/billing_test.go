package billing_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v87"

	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing/stripetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	webURL  = "https://civic.example"
	priceID = "price_test123"
)

type memoryStore struct {
	mu        sync.Mutex
	customers map[uuid.UUID]string
	getErr    error
	setErr    error
	clearErr  error
	sets      int
	clears    int
}

func newMemoryStore() *memoryStore { return &memoryStore{customers: map[uuid.UUID]string{}} }

func (m *memoryStore) StripeCustomerID(_ context.Context, userID uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.customers[userID], m.getErr
}

func (m *memoryStore) ClearStripeCustomerID(_ context.Context, userID uuid.UUID, customerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clears++
	if m.clearErr != nil {
		return m.clearErr
	}
	if m.customers[userID] == customerID {
		delete(m.customers, userID)
	}
	return nil
}

func (m *memoryStore) SetStripeCustomerID(_ context.Context, userID uuid.UUID, customerID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sets++
	if m.setErr != nil {
		return "", m.setErr
	}
	if existing := m.customers[userID]; existing != "" {
		return existing, nil
	}
	m.customers[userID] = customerID
	return customerID, nil
}

type harness struct {
	stripe *stripetest.Server
	store  *memoryStore
	svc    *billing.Service
	logs   *bytes.Buffer
}

type syncBuffer struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func newHarness(t *testing.T, opts ...billing.Option) harness {
	t.Helper()
	fake := stripetest.New(t)
	mem := newMemoryStore()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(&syncBuffer{buf: logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	all := append([]billing.Option{billing.WithAPIURL(fake.URL), billing.WithoutRetryDelay(), billing.WithLogger(logger)}, opts...)
	svc := billing.New(config.Stripe{SecretKey: config.NewSecret(stripetest.SecretKey), PriceID: priceID}, webURL, mem, all...)
	return harness{stripe: fake, store: mem, svc: svc, logs: logs}
}

func testUser(email string) store.User {
	u := store.User{ID: uuid.New(), WorkOSUserID: "user_" + uuid.NewString()}
	if email != "" {
		u.Email = &email
	}
	return u
}

func TestNotConfiguredNeverCallsStripe(t *testing.T) {
	fake := stripetest.New(t)
	mem := newMemoryStore()
	for name, svc := range map[string]*billing.Service{
		"no key":     billing.New(config.Stripe{}, webURL, mem, billing.WithAPIURL(fake.URL)),
		"no web url": billing.New(config.Stripe{SecretKey: config.NewSecret(stripetest.SecretKey), PriceID: priceID}, "", mem, billing.WithAPIURL(fake.URL)),
		"no store":   billing.New(config.Stripe{SecretKey: config.NewSecret(stripetest.SecretKey), PriceID: priceID}, webURL, nil, billing.WithAPIURL(fake.URL)),
		"zero":       {},
		"nil":        nil,
	} {
		if svc.Configured() {
			t.Errorf("%s: Configured() = true", name)
		}
		if _, err := svc.StartCheckout(t.Context(), testUser("")); !errors.Is(err, billing.ErrNotConfigured) {
			t.Errorf("%s: StartCheckout err = %v, want ErrNotConfigured", name, err)
		}
		if _, err := svc.OpenPortal(t.Context(), testUser("")); !errors.Is(err, billing.ErrNotConfigured) {
			t.Errorf("%s: OpenPortal err = %v, want ErrNotConfigured", name, err)
		}
	}
	if n := len(fake.Requests()); n != 0 {
		t.Errorf("Stripe got %d requests, want none", n)
	}
	if mem.sets != 0 {
		t.Errorf("store was written %d times", mem.sets)
	}
}

func TestStartCheckoutCreatesTheCustomerThenTheSession(t *testing.T) {
	h := newHarness(t)
	user := testUser("ada@example.com")

	link, err := h.svc.StartCheckout(t.Context(), user)
	if err != nil {
		t.Fatalf("StartCheckout: %v", err)
	}
	if !strings.HasPrefix(link, "https://checkout.stripe.test/c/pay/cs_test_") {
		t.Errorf("url = %q", link)
	}

	reqs := h.stripe.Requests()
	if len(reqs) != 2 || reqs[0].Path != "/v1/customers" || reqs[1].Path != "/v1/checkout/sessions" {
		t.Fatalf("requests = %+v, want a customer then a checkout session", reqs)
	}
	customer := reqs[0]
	if customer.IdempotencyKey != billing.CustomerIdempotencyKey(user, "") || !strings.HasPrefix(customer.IdempotencyKey, "civic-scanner-customer-"+user.ID.String()+"-") {
		t.Errorf("customer idempotency key = %q", customer.IdempotencyKey)
	}
	if got := customer.Form.Get("email"); got != "ada@example.com" {
		t.Errorf("customer email = %q", got)
	}
	if got := customer.Form.Get("metadata[civic_scanner_user_id]"); got != user.ID.String() {
		t.Errorf("customer metadata user id = %q", got)
	}

	stored := h.store.customers[user.ID]
	if stored == "" || h.stripe.Customers()[stored] != user.ID.String() {
		t.Fatalf("stored customer %q, fake customers %v", stored, h.stripe.Customers())
	}

	session := reqs[1]
	want := map[string]string{
		"mode":                            "subscription",
		"customer":                        stored,
		"client_reference_id":             user.ID.String(),
		"line_items[0][price]":            priceID,
		"line_items[0][quantity]":         "1",
		"success_url":                     webURL + "/billing/success",
		"cancel_url":                      webURL + "/billing/cancel",
		"metadata[civic_scanner_user_id]": user.ID.String(),
		"subscription_data[metadata][civic_scanner_user_id]": user.ID.String(),
	}
	for key, value := range want {
		if got := session.Form.Get(key); got != value {
			t.Errorf("checkout %s = %q, want %q", key, got, value)
		}
	}
	if len(session.Form) != len(want) {
		t.Errorf("checkout form = %v, want exactly %v", session.Form, want)
	}
	if !strings.HasPrefix(session.IdempotencyKey, "civic-scanner-checkout-"+user.ID.String()+"-") {
		t.Errorf("checkout idempotency key = %q", session.IdempotencyKey)
	}
	for _, r := range reqs {
		if r.StripeVersion != stripe.APIVersion {
			t.Errorf("%s Stripe-Version = %q, want %q", r.Path, r.StripeVersion, stripe.APIVersion)
		}
	}
	if !strings.Contains(h.logs.String(), `"msg":"checkout session created"`) {
		t.Errorf("logs = %s", h.logs.String())
	}
}

func TestStartCheckoutWithoutEmailLeavesItToCheckout(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.StartCheckout(t.Context(), testUser("")); err != nil {
		t.Fatal(err)
	}
	if form := h.stripe.RequestsTo("/v1/customers")[0].Form; form.Has("email") {
		t.Errorf("customer form = %v, want no email", form)
	}
}

func TestStartCheckoutReusesTheStoredCustomer(t *testing.T) {
	h := newHarness(t)
	user := testUser("ada@example.com")
	h.store.customers[user.ID] = "cus_existing"
	h.stripe.AddCustomer("cus_existing", user.ID.String())
	h.stripe.AddSubscription("cus_existing", "canceled")
	h.stripe.AddSubscription("cus_existing", "incomplete_expired")
	h.stripe.AddSubscription("cus_existing", "incomplete")
	h.stripe.AddSubscription("cus_other", "active")

	first, err := h.svc.StartCheckout(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.StartCheckout(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Errorf("both checkouts returned %q, want a fresh session each time", first)
	}
	if n := len(h.stripe.RequestsTo("/v1/customers")); n != 0 {
		t.Errorf("created %d customers, want none", n)
	}
	lists := h.stripe.RequestsTo("/v1/subscriptions")
	if len(lists) != 2 || lists[0].Form.Get("customer") != "cus_existing" || lists[0].Form.Get("status") != "all" {
		t.Errorf("subscription lists = %+v", lists)
	}
	sessions := h.stripe.RequestsTo("/v1/checkout/sessions")
	if len(sessions) != 2 || sessions[0].Form.Get("customer") != "cus_existing" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if sessions[0].IdempotencyKey == sessions[1].IdempotencyKey {
		t.Errorf("both sessions used idempotency key %q", sessions[0].IdempotencyKey)
	}
}

func TestStartCheckoutRefusesALiveSubscription(t *testing.T) {
	for _, status := range []string{"active", "trialing", "past_due", "unpaid", "paused"} {
		h := newHarness(t)
		user := testUser("")
		h.store.customers[user.ID] = "cus_live"
		h.stripe.AddCustomer("cus_live", user.ID.String())
		h.stripe.AddSubscription("cus_live", "canceled")
		h.stripe.AddSubscription("cus_live", status)

		_, err := h.svc.StartCheckout(t.Context(), user)
		if !errors.Is(err, billing.ErrAlreadySubscribed) {
			t.Errorf("%s: err = %v, want ErrAlreadySubscribed", status, err)
		}
		if n := len(h.stripe.RequestsTo("/v1/checkout/sessions")); n != 0 {
			t.Errorf("%s: created %d sessions", status, n)
		}
	}
}

func TestOpenPortal(t *testing.T) {
	h := newHarness(t)
	user := testUser("")
	h.store.customers[user.ID] = "cus_portal"
	h.stripe.AddCustomer("cus_portal", user.ID.String())

	link, err := h.svc.OpenPortal(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "https://billing.stripe.test/p/session/bps_test_") {
		t.Errorf("url = %q", link)
	}
	reqs := h.stripe.Requests()
	if len(reqs) != 1 || reqs[0].Path != "/v1/billing_portal/sessions" {
		t.Fatalf("requests = %+v", reqs)
	}
	if got := reqs[0].Form.Get("customer"); got != "cus_portal" {
		t.Errorf("customer = %q", got)
	}
	if got := reqs[0].Form.Get("return_url"); got != webURL+"/billing" {
		t.Errorf("return_url = %q", got)
	}
	if len(reqs[0].Form) != 2 {
		t.Errorf("form = %v, want customer and return_url only", reqs[0].Form)
	}
	if !strings.HasPrefix(reqs[0].IdempotencyKey, "civic-scanner-portal-"+user.ID.String()+"-") {
		t.Errorf("idempotency key = %q", reqs[0].IdempotencyKey)
	}
	if strings.Contains(h.logs.String(), link) {
		t.Errorf("the portal URL was logged: %s", h.logs.String())
	}
}

func TestOpenPortalWithoutCustomerIsAConflictWithoutCallingStripe(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.OpenPortal(t.Context(), testUser("")); !errors.Is(err, billing.ErrNoCustomer) {
		t.Errorf("err = %v, want ErrNoCustomer", err)
	}
	if n := len(h.stripe.Requests()); n != 0 {
		t.Errorf("Stripe got %d requests", n)
	}
}

func TestStripeErrorsAreGenericAndLoggedWithoutSecrets(t *testing.T) {
	cases := map[string]struct {
		path    string
		failure stripetest.Failure
		prepare func(h harness, user store.User)
		call    func(h harness, user store.User) error
	}{
		"customer": {
			path:    "/v1/customers",
			failure: stripetest.Failure{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "parameter_invalid_string"},
			call: func(h harness, u store.User) error {
				_, err := h.svc.StartCheckout(context.Background(), u)
				return err
			},
		},
		"checkout": {
			path:    "/v1/checkout/sessions",
			failure: stripetest.Failure{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "resource_missing"},
			call: func(h harness, u store.User) error {
				_, err := h.svc.StartCheckout(context.Background(), u)
				return err
			},
		},
		"subscriptions": {
			path:    "/v1/subscriptions",
			failure: stripetest.Failure{Status: http.StatusTooManyRequests, Type: "invalid_request_error", Code: "rate_limit"},
			prepare: func(h harness, u store.User) {
				h.store.customers[u.ID] = "cus_known"
				h.stripe.AddCustomer("cus_known", "")
			},
			call: func(h harness, u store.User) error {
				_, err := h.svc.StartCheckout(context.Background(), u)
				return err
			},
		},
		"portal": {
			path:    "/v1/billing_portal/sessions",
			failure: stripetest.Failure{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "resource_missing"},
			prepare: func(h harness, u store.User) {
				h.store.customers[u.ID] = "cus_known"
				h.stripe.AddCustomer("cus_known", "")
			},
			call: func(h harness, u store.User) error { _, err := h.svc.OpenPortal(context.Background(), u); return err },
		},
	}
	for name, tc := range cases {
		h := newHarness(t)
		user := testUser("")
		if tc.prepare != nil {
			tc.prepare(h, user)
		}
		h.stripe.FailNext(tc.path, tc.failure)

		err := tc.call(h, user)
		if !errors.Is(err, billing.ErrUnavailable) {
			t.Errorf("%s: err = %v, want ErrUnavailable", name, err)
		}
		if strings.Contains(err.Error(), "stripetest") {
			t.Errorf("%s: the error carries Stripe's message: %v", name, err)
		}
		logs := h.logs.String()
		for _, want := range []string{`"msg":"stripe request failed"`, `"code":"` + tc.failure.Code + `"`, `"request_id":"req_stripetest"`, `"type":"invalid_request_error"`} {
			if !strings.Contains(logs, want) {
				t.Errorf("%s: logs lack %s: %s", name, want, logs)
			}
		}
		if strings.Contains(logs, stripetest.SecretKey) || strings.Contains(logs, "stripetest bad request") {
			t.Errorf("%s: logs show the key or Stripe's message: %s", name, logs)
		}
		if n := len(h.stripe.RequestsTo(tc.path)); n != 1 {
			t.Errorf("%s: %d requests to %s, want 1 (a 4xx is not retried)", name, n, tc.path)
		}
	}
}

func TestServerErrorsAreRetriedOnceWithTheSameIdempotencyKey(t *testing.T) {
	h := newHarness(t)
	user := testUser("")
	h.stripe.FailNext("/v1/customers", stripetest.Failure{Status: http.StatusInternalServerError, Type: "api_error"})
	h.stripe.FailNext("/v1/checkout/sessions", stripetest.Failure{Status: http.StatusServiceUnavailable, Type: "api_error"})

	if _, err := h.svc.StartCheckout(t.Context(), user); err != nil {
		t.Fatalf("StartCheckout after one failure each: %v", err)
	}
	for _, path := range []string{"/v1/customers", "/v1/checkout/sessions"} {
		reqs := h.stripe.RequestsTo(path)
		if len(reqs) != 2 || reqs[0].IdempotencyKey == "" || reqs[0].IdempotencyKey != reqs[1].IdempotencyKey {
			t.Errorf("%s: requests = %+v, want two with one idempotency key", path, reqs)
		}
	}

	h = newHarness(t)
	h.stripe.FailNext("/v1/customers",
		stripetest.Failure{Status: http.StatusInternalServerError, Type: "api_error"},
		stripetest.Failure{Status: http.StatusInternalServerError, Type: "api_error"},
		stripetest.Failure{Status: http.StatusInternalServerError, Type: "api_error"},
	)
	if _, err := h.svc.StartCheckout(t.Context(), user); !errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if n := len(h.stripe.RequestsTo("/v1/customers")); n != 2 {
		t.Errorf("%d attempts, want 2 (one retry)", n)
	}
	if len(h.store.customers) != 0 {
		t.Errorf("stored %v after a failure", h.store.customers)
	}
}

func TestTimeoutsAreBoundedAndGeneric(t *testing.T) {
	h := newHarness(t, billing.WithHTTPClient(&http.Client{Timeout: 20 * time.Millisecond}))
	h.stripe.Hang("/v1/customers")

	_, err := h.svc.StartCheckout(t.Context(), testUser(""))
	if !errors.Is(err, billing.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if n := len(h.stripe.RequestsTo("/v1/customers")); n != 2 {
		t.Errorf("%d attempts, want 2", n)
	}
	if strings.Contains(h.logs.String(), stripetest.SecretKey) {
		t.Errorf("logs show the key: %s", h.logs.String())
	}
}

func TestACanceledRequestStopsWithoutRetrying(t *testing.T) {
	h := newHarness(t)
	h.stripe.Hang("/v1/billing_portal/sessions")
	user := testUser("")
	h.store.customers[user.ID] = "cus_known"

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		for len(h.stripe.RequestsTo("/v1/billing_portal/sessions")) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, err := h.svc.OpenPortal(ctx, user); !errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if n := len(h.stripe.RequestsTo("/v1/billing_portal/sessions")); n != 1 {
		t.Errorf("%d attempts after cancel, want 1", n)
	}
}

func TestResponsesWithoutAnHTTPSURLOrIDAreFailures(t *testing.T) {
	h := newHarness(t)
	h.stripe.ReturnSessionURL("")
	user := testUser("")
	if _, err := h.svc.StartCheckout(t.Context(), user); !errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("checkout err = %v, want ErrUnavailable", err)
	}
	if _, err := h.svc.OpenPortal(t.Context(), user); !errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("portal err = %v, want ErrUnavailable", err)
	}

	h = newHarness(t)
	h.stripe.ReturnCustomerWithoutID()
	if _, err := h.svc.StartCheckout(t.Context(), testUser("")); !errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("customer without id: err = %v, want ErrUnavailable", err)
	}
	if h.store.sets != 0 {
		t.Errorf("stored a customer without an id")
	}
}

func TestStoreErrorsAreNotStripeErrors(t *testing.T) {
	boom := errors.New("database down")

	h := newHarness(t)
	h.store.getErr = boom
	if _, err := h.svc.StartCheckout(t.Context(), testUser("")); !errors.Is(err, boom) || errors.Is(err, billing.ErrUnavailable) {
		t.Errorf("checkout read: err = %v", err)
	}
	if _, err := h.svc.OpenPortal(t.Context(), testUser("")); !errors.Is(err, boom) {
		t.Errorf("portal read: err = %v", err)
	}
	if n := len(h.stripe.Requests()); n != 0 {
		t.Errorf("Stripe got %d requests after a read failure", n)
	}

	h = newHarness(t)
	h.store.setErr = boom
	if _, err := h.svc.StartCheckout(t.Context(), testUser("")); !errors.Is(err, boom) {
		t.Errorf("checkout write: err = %v", err)
	}
	if n := len(h.stripe.RequestsTo("/v1/checkout/sessions")); n != 0 {
		t.Errorf("created a session without a stored customer")
	}
}

func TestConcurrentCheckoutsShareOneCustomer(t *testing.T) {
	for _, ignoreKeys := range []bool{false, true} {
		h := newHarness(t)
		if ignoreKeys {
			h.stripe.IgnoreIdempotencyKeys()
		}
		user := testUser("ada@example.com")
		start := make(chan struct{})
		const n = 8
		h.stripe.OnCustomerCreate(stripetest.Barrier(n))
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for range n {
			wg.Go(func() {
				<-start
				_, err := h.svc.StartCheckout(t.Context(), user)
				errs <- err
			})
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("ignore keys %v: %v", ignoreKeys, err)
			}
		}

		stored := h.store.customers[user.ID]
		keys := map[string]bool{}
		for _, r := range h.stripe.RequestsTo("/v1/customers") {
			keys[r.IdempotencyKey] = true
		}
		if len(keys) != 1 || !keys[billing.CustomerIdempotencyKey(user, "")] {
			t.Errorf("ignore keys %v: customer idempotency keys %v", ignoreKeys, keys)
		}
		if !ignoreKeys && len(h.stripe.Customers()) != 1 {
			t.Errorf("Stripe has %d customers, want 1", len(h.stripe.Customers()))
		}
		for _, r := range h.stripe.RequestsTo("/v1/checkout/sessions") {
			if got := r.Form.Get("customer"); got != stored {
				t.Errorf("ignore keys %v: a session used %q, stored %q", ignoreKeys, got, stored)
			}
		}
		if ignoreKeys && len(h.stripe.Customers()) != n {
			t.Errorf("Stripe has %d customers without idempotency, want %d", len(h.stripe.Customers()), n)
		}
		if ignoreKeys && !strings.Contains(h.logs.String(), "another request stored a different stripe customer first") {
			t.Errorf("the lost race was not logged: %s", h.logs.String())
		}
	}
}
