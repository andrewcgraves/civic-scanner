//go:build integration

package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestStripeCustomerIDIsSetOnce(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workOSUserID(t, pool)})
	if err != nil {
		t.Fatal(err)
	}

	if got, err := s.StripeCustomerID(ctx, user.ID); err != nil || got != "" {
		t.Fatalf("new user: (%q, %v), want no customer", got, err)
	}
	customer := "cus_store_" + uuid.NewString()
	if got, err := s.SetStripeCustomerID(ctx, user.ID, customer); err != nil || got != customer {
		t.Fatalf("first set: (%q, %v)", got, err)
	}
	if got, err := s.SetStripeCustomerID(ctx, user.ID, "cus_store_other_"+uuid.NewString()); err != nil || got != customer {
		t.Errorf("second set: (%q, %v), want the first customer kept", got, err)
	}
	if got, err := s.StripeCustomerID(ctx, user.ID); err != nil || got != customer {
		t.Errorf("read back: (%q, %v)", got, err)
	}

	if err := s.ClearStripeCustomerID(ctx, user.ID, "cus_store_not_the_stored_one"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StripeCustomerID(ctx, user.ID); got != customer {
		t.Errorf("clearing another id removed the stored one: %q", got)
	}
	if err := s.ClearStripeCustomerID(ctx, user.ID, customer); err != nil {
		t.Fatal(err)
	}
	if got, err := s.StripeCustomerID(ctx, user.ID); err != nil || got != "" {
		t.Errorf("after clear: (%q, %v), want none", got, err)
	}
	if got, err := s.SetStripeCustomerID(ctx, user.ID, customer); err != nil || got != customer {
		t.Errorf("set after clear: (%q, %v)", got, err)
	}

	other, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workOSUserID(t, pool)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStripeCustomerID(ctx, other.ID, customer); err == nil {
		t.Error("a second user took the same customer")
	}
	if _, err := s.SetStripeCustomerID(ctx, other.ID, ""); !errors.Is(err, store.ErrInvalidStripeCustomer) {
		t.Errorf("empty customer: err = %v", err)
	}
}

func TestStripeCustomerIDUnknownUser(t *testing.T) {
	s := store.New(testdb.Pool(t))
	if _, err := s.StripeCustomerID(t.Context(), uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("read: err = %v, want ErrNotFound", err)
	}
	if _, err := s.SetStripeCustomerID(t.Context(), uuid.New(), "cus_store_nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("set: err = %v, want ErrNotFound", err)
	}
}

func TestStripeCustomerIDConcurrentSetsAgree(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workOSUserID(t, pool)})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	prefix := "cus_store_race_" + uuid.NewString()[:8]
	results := make([]string, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			got, err := s.SetStripeCustomerID(ctx, user.ID, fmt.Sprintf("%s_%d", prefix, i))
			if err != nil {
				t.Errorf("worker %d: %v", i, err)
			}
			results[i] = got
		})
	}
	wg.Wait()
	for i, got := range results {
		if got != results[0] {
			t.Errorf("worker %d got %q, worker 0 got %q", i, got, results[0])
		}
	}
	if stored, _ := s.StripeCustomerID(ctx, user.ID); stored != results[0] {
		t.Errorf("stored %q, workers agreed on %q", stored, results[0])
	}
}
