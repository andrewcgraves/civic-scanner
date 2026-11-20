//go:build integration

package billing_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing/stripetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestConcurrentCheckoutsAgainstPostgresKeepOneCustomer(t *testing.T) {
	for _, ignoreKeys := range []bool{false, true} {
		ctx := t.Context()
		pool := testdb.Pool(t)
		db := store.New(pool)
		workosID := "user_billing_" + uuid.NewString()
		t.Cleanup(func() {
			_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id = $1`, workosID)
		})
		user, _, err := db.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID, Email: "ada@example.com"})
		if err != nil {
			t.Fatal(err)
		}

		fake := stripetest.New(t)
		if ignoreKeys {
			fake.IgnoreIdempotencyKeys()
		}
		const workers = 6
		fake.OnCustomerCreate(stripetest.Barrier(workers))
		svc := billing.New(config.Stripe{SecretKey: config.NewSecret(stripetest.SecretKey), PriceID: priceID}, webURL, db, billing.WithAPIURL(fake.URL), billing.WithoutRetryDelay())

		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				if _, err := svc.StartCheckout(ctx, user); err != nil {
					t.Errorf("ignore keys %v: %v", ignoreKeys, err)
				}
			})
		}
		wg.Wait()

		stored, err := db.StripeCustomerID(ctx, user.ID)
		if err != nil || stored == "" {
			t.Fatalf("stored (%q, %v)", stored, err)
		}
		want := 1
		if ignoreKeys {
			want = workers
		}
		if got := len(fake.Customers()); got != want {
			t.Errorf("ignore keys %v: Stripe has %d customers, want %d", ignoreKeys, got, want)
		}
		sessions := fake.RequestsTo("/v1/checkout/sessions")
		if len(sessions) != workers {
			t.Errorf("ignore keys %v: %d sessions, want %d", ignoreKeys, len(sessions), workers)
		}
		for _, r := range sessions {
			if r.Form.Get("customer") != stored {
				t.Errorf("ignore keys %v: session for %q, stored %q", ignoreKeys, r.Form.Get("customer"), stored)
			}
		}

		if _, err := svc.StartCheckout(ctx, user); err != nil {
			t.Fatal(err)
		}
		if got := len(fake.Customers()); got != want {
			t.Errorf("ignore keys %v: a later checkout created a customer", ignoreKeys)
		}
		if _, err := svc.OpenPortal(ctx, user); err != nil {
			t.Errorf("portal: %v", err)
		}
	}
}

func TestConcurrentRecoveryAgainstPostgresKeepsOneNewCustomer(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	db := store.New(pool)
	workosID := "user_billing_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id = $1`, workosID)
	})
	user, _, err := db.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID})
	if err != nil {
		t.Fatal(err)
	}
	dead := "cus_deleted_" + uuid.NewString()[:8]
	if _, err := db.SetStripeCustomerID(ctx, user.ID, dead); err != nil {
		t.Fatal(err)
	}

	fake := stripetest.New(t)
	const workers = 5
	fake.OnCustomerCreate(stripetest.Barrier(workers))
	svc := billing.New(config.Stripe{SecretKey: config.NewSecret(stripetest.SecretKey), PriceID: priceID}, webURL, db, billing.WithAPIURL(fake.URL), billing.WithoutRetryDelay())

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			if _, err := svc.StartCheckout(ctx, user); err != nil {
				t.Errorf("StartCheckout: %v", err)
			}
		})
	}
	wg.Wait()

	stored, err := db.StripeCustomerID(ctx, user.ID)
	if err != nil || stored == "" || stored == dead {
		t.Fatalf("stored (%q, %v)", stored, err)
	}
	if fake.Customers()[stored] == "" {
		t.Errorf("stored %q is not a Stripe customer: %v", stored, fake.Customers())
	}
	for _, r := range fake.RequestsTo("/v1/checkout/sessions") {
		if r.Form.Get("customer") != stored {
			t.Errorf("session for %q, stored %q", r.Form.Get("customer"), stored)
		}
	}
}
