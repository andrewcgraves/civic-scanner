//go:build integration

package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type planFixture struct {
	pool     *pgxpool.Pool
	store    *store.Store
	user     store.User
	customer string
	base     time.Time
}

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workOSUserID(t, pool)})
	if err != nil {
		t.Fatal(err)
	}
	customer := "cus_plan_" + uuid.NewString()
	if _, err := s.SetStripeCustomerID(ctx, user.ID, customer); err != nil {
		t.Fatal(err)
	}
	return &planFixture{pool: pool, store: s, user: user, customer: customer, base: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
}

func newEventID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	id := "evt_store_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM stripe_events WHERE id = $1`, id)
	})
	return id
}

func (f *planFixture) apply(t *testing.T, id string, offset time.Duration, sub *store.SubscriptionState) store.StripeEventResult {
	t.Helper()
	result, err := f.store.ApplyStripeEvent(t.Context(), store.StripeEvent{ID: id, Type: "customer.subscription.updated", CreatedAt: f.base.Add(offset)}, sub)
	if err != nil {
		t.Fatalf("apply %s: %v", id, err)
	}
	return result
}

func (f *planFixture) sub(id, status string, paid bool) *store.SubscriptionState {
	return &store.SubscriptionState{ID: id, CustomerID: f.customer, Status: status, Paid: paid}
}

func (f *planFixture) plan(t *testing.T) store.Plan {
	t.Helper()
	user, _, err := f.store.EnsureUser(t.Context(), store.UserInput{WorkOSUserID: f.user.WorkOSUserID})
	if err != nil {
		t.Fatal(err)
	}
	return user.Plan
}

func TestNewUsersAreFree(t *testing.T) {
	f := newPlanFixture(t)
	if f.user.Plan != store.PlanFree || f.plan(t) != store.PlanFree {
		t.Errorf("plan = %q, want free", f.user.Plan)
	}
}

func TestApplyStripeEventSetsThePlan(t *testing.T) {
	f := newPlanFixture(t)
	sub := "sub_store_" + uuid.NewString()

	result := f.apply(t, newEventID(t, f.pool), 0, f.sub(sub, "active", true))
	if result.Outcome != store.StripeEventApplied || result.UserID != f.user.ID || result.PreviousPlan != store.PlanFree || result.Plan != store.PlanPaid || result.PaidSubscriptions != 1 {
		t.Errorf("result = %+v", result)
	}
	if f.plan(t) != store.PlanPaid {
		t.Error("plan not paid after an active subscription")
	}

	result = f.apply(t, newEventID(t, f.pool), time.Second, f.sub(sub, "canceled", false))
	if result.Outcome != store.StripeEventApplied || result.PreviousPlan != store.PlanPaid || result.Plan != store.PlanFree {
		t.Errorf("cancel: result = %+v", result)
	}
	if f.plan(t) != store.PlanFree {
		t.Error("plan still paid after cancel")
	}
}

func TestApplyStripeEventIgnoresAReplay(t *testing.T) {
	f := newPlanFixture(t)
	sub := "sub_store_" + uuid.NewString()
	id := newEventID(t, f.pool)

	f.apply(t, id, 0, f.sub(sub, "active", true))
	f.apply(t, newEventID(t, f.pool), time.Second, f.sub(sub, "canceled", false))
	result := f.apply(t, id, 2*time.Second, f.sub(sub, "active", true))
	if result.Outcome != store.StripeEventDuplicate {
		t.Errorf("replay: outcome = %s, want duplicate", result.Outcome)
	}
	if f.plan(t) != store.PlanFree {
		t.Error("a replayed event changed the plan")
	}
	var processed int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM stripe_events WHERE id = $1`, id).Scan(&processed); err != nil || processed != 1 {
		t.Errorf("stripe_events rows for the event = %d, %v", processed, err)
	}
}

func TestApplyStripeEventConcurrentRedeliveriesApplyOnce(t *testing.T) {
	f := newPlanFixture(t)
	id := newEventID(t, f.pool)
	sub := f.sub("sub_store_"+uuid.NewString(), "active", true)

	const n = 8
	outcomes := make([]store.StripeEventOutcome, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			result, err := f.store.ApplyStripeEvent(t.Context(), store.StripeEvent{ID: id, Type: "customer.subscription.created", CreatedAt: f.base}, sub)
			if err != nil {
				t.Error(err)
			}
			outcomes[i] = result.Outcome
		})
	}
	wg.Wait()
	applied := 0
	for _, o := range outcomes {
		switch o {
		case store.StripeEventApplied:
			applied++
		case store.StripeEventDuplicate:
		default:
			t.Errorf("outcome %s", o)
		}
	}
	if applied != 1 {
		t.Errorf("applied %d times, want once: %v", applied, outcomes)
	}
}

func TestApplyStripeEventIgnoresAnOlderEvent(t *testing.T) {
	f := newPlanFixture(t)
	sub := "sub_store_" + uuid.NewString()

	f.apply(t, newEventID(t, f.pool), 10*time.Second, f.sub(sub, "unpaid", false))
	result := f.apply(t, newEventID(t, f.pool), 5*time.Second, f.sub(sub, "active", true))
	if result.Outcome != store.StripeEventStale || result.Plan != store.PlanFree {
		t.Errorf("older event: result = %+v, want stale and free", result)
	}
	var status string
	if err := f.pool.QueryRow(t.Context(), `SELECT status FROM stripe_subscriptions WHERE id = $1`, sub).Scan(&status); err != nil || status != "unpaid" {
		t.Errorf("stored status = %q, %v; want unpaid", status, err)
	}

	result = f.apply(t, newEventID(t, f.pool), 10*time.Second, f.sub(sub, "active", true))
	if result.Outcome != store.StripeEventApplied || result.Plan != store.PlanPaid {
		t.Errorf("same-second event: result = %+v, want applied", result)
	}
}

func TestApplyStripeEventNeverRevivesAnEndedSubscription(t *testing.T) {
	for _, ended := range []string{"canceled", "incomplete_expired"} {
		f := newPlanFixture(t)
		sub := "sub_store_" + uuid.NewString()
		f.apply(t, newEventID(t, f.pool), 0, f.sub(sub, ended, false))
		result := f.apply(t, newEventID(t, f.pool), time.Minute, f.sub(sub, "active", true))
		if result.Outcome != store.StripeEventStale || f.plan(t) != store.PlanFree {
			t.Errorf("%s then active: result = %+v, plan %s", ended, result, f.plan(t))
		}
	}
}

func TestApplyStripeEventKeepsThePlanWhileAnySubscriptionIsPaid(t *testing.T) {
	f := newPlanFixture(t)
	first, second := "sub_store_a_"+uuid.NewString(), "sub_store_b_"+uuid.NewString()

	f.apply(t, newEventID(t, f.pool), 0, f.sub(first, "active", true))
	result := f.apply(t, newEventID(t, f.pool), time.Second, f.sub(second, "active", true))
	if result.PaidSubscriptions != 2 || result.Plan != store.PlanPaid {
		t.Errorf("two paid: result = %+v", result)
	}
	result = f.apply(t, newEventID(t, f.pool), 2*time.Second, f.sub(first, "canceled", false))
	if result.PaidSubscriptions != 1 || result.Plan != store.PlanPaid {
		t.Errorf("one cancelled: result = %+v, want still paid", result)
	}
	result = f.apply(t, newEventID(t, f.pool), 3*time.Second, f.sub(second, "canceled", false))
	if result.PaidSubscriptions != 0 || result.Plan != store.PlanFree {
		t.Errorf("both cancelled: result = %+v, want free", result)
	}
}

func TestApplyStripeEventUnknownCustomerAndIgnoredEvents(t *testing.T) {
	f := newPlanFixture(t)
	stranger := &store.SubscriptionState{ID: "sub_store_" + uuid.NewString(), CustomerID: "cus_nobody_" + uuid.NewString(), Status: "active", Paid: true}
	id := newEventID(t, f.pool)
	if result := f.apply(t, id, 0, stranger); result.Outcome != store.StripeEventUnknownCustomer || result.UserID != uuid.Nil {
		t.Errorf("unknown customer: %+v", result)
	}
	if result := f.apply(t, id, 0, stranger); result.Outcome != store.StripeEventDuplicate {
		t.Errorf("unknown customer redelivered: %+v, want duplicate", result)
	}
	var rows int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM stripe_subscriptions WHERE id = $1`, stranger.ID).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a subscription row was stored for an unknown customer: %d, %v", rows, err)
	}

	ignored := newEventID(t, f.pool)
	if result := f.apply(t, ignored, 0, nil); result.Outcome != store.StripeEventIgnored {
		t.Errorf("no subscription: %+v", result)
	}
	if result := f.apply(t, ignored, 0, nil); result.Outcome != store.StripeEventDuplicate {
		t.Errorf("no subscription redelivered: %+v", result)
	}
}

func TestClearingTheCustomerMakesTheUserFree(t *testing.T) {
	f := newPlanFixture(t)
	sub := "sub_store_" + uuid.NewString()
	f.apply(t, newEventID(t, f.pool), 0, f.sub(sub, "active", true))
	if err := f.store.ClearStripeCustomerID(t.Context(), f.user.ID, f.customer); err != nil {
		t.Fatal(err)
	}
	if f.plan(t) != store.PlanFree {
		t.Error("plan still paid after the customer was cleared")
	}

	next := "cus_plan_next_" + uuid.NewString()
	if _, err := f.store.SetStripeCustomerID(t.Context(), f.user.ID, next); err != nil {
		t.Fatal(err)
	}
	f.customer = next
	result := f.apply(t, newEventID(t, f.pool), time.Second, f.sub("sub_store_other_"+uuid.NewString(), "canceled", false))
	if result.Plan != store.PlanFree || result.PaidSubscriptions != 0 {
		t.Errorf("the old customer's subscription still counts: %+v", result)
	}
}

func TestApplyStripeEventRejectsIncompleteInput(t *testing.T) {
	f := newPlanFixture(t)
	for name, tc := range map[string]struct {
		event store.StripeEvent
		sub   *store.SubscriptionState
	}{
		"no id":       {store.StripeEvent{Type: "x", CreatedAt: f.base}, nil},
		"no type":     {store.StripeEvent{ID: "evt_x", CreatedAt: f.base}, nil},
		"no time":     {store.StripeEvent{ID: "evt_x", Type: "x"}, nil},
		"no sub id":   {store.StripeEvent{ID: "evt_x", Type: "x", CreatedAt: f.base}, &store.SubscriptionState{CustomerID: "cus", Status: "active"}},
		"no customer": {store.StripeEvent{ID: "evt_x", Type: "x", CreatedAt: f.base}, &store.SubscriptionState{ID: "sub", Status: "active"}},
		"no status":   {store.StripeEvent{ID: "evt_x", Type: "x", CreatedAt: f.base}, &store.SubscriptionState{ID: "sub", CustomerID: "cus"}},
	} {
		if _, err := f.store.ApplyStripeEvent(t.Context(), tc.event, tc.sub); !errors.Is(err, store.ErrInvalidStripeEvent) {
			t.Errorf("%s: err = %v, want ErrInvalidStripeEvent", name, err)
		}
	}
}
