//go:build integration

package migrations

import (
	"testing"
)

func TestStripePlanSyncMigration(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.UpTo(ctx, 14); err != nil {
		t.Fatalf("UpTo(14): %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (workos_user_id, stripe_customer_id) VALUES ('user_before_plans', 'cus_before_plans')`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 15); err != nil {
		t.Fatalf("UpTo(15): %v", err)
	}

	var plan string
	if err := pool.QueryRow(ctx, `SELECT plan FROM users WHERE workos_user_id = 'user_before_plans'`).Scan(&plan); err != nil || plan != "free" {
		t.Errorf("existing user plan = %q, %v; want free", plan, err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_after_plans') RETURNING plan, id`).Scan(&plan, &userID); err != nil || plan != "free" {
		t.Errorf("new user plan = %q, %v; want free", plan, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET plan = 'paid' WHERE id = $1`, userID); err != nil {
		t.Errorf("paid plan rejected: %v", err)
	}
	for value, want := range map[any][2]string{
		"pro":  {"23514", "users_plan_check"},
		"Paid": {"23514", "users_plan_check"},
		"":     {"23514", "users_plan_check"},
		nil:    {"23502", ""},
	} {
		_, err := pool.Exec(ctx, `UPDATE users SET plan = $1 WHERE id = $2`, value, userID)
		if code, constraint := pgCode(err); code != want[0] || constraint != want[1] {
			t.Errorf("plan %v: err = %v, want code %s on %q", value, err, want[0], want[1])
		}
	}

	if _, err := pool.Exec(ctx, `INSERT INTO stripe_events (id, type, created_at) VALUES ('evt_m1', 'customer.subscription.created', now())`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO stripe_events (id, type, created_at) VALUES ('evt_m1', 'customer.subscription.updated', now())`)
	if code, constraint := pgCode(err); code != "23505" || constraint != "stripe_events_pkey" {
		t.Errorf("duplicate event id: err = %v, want stripe_events_pkey", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO stripe_events (id, type, created_at) VALUES ('', 'x', now())`)
	if _, constraint := pgCode(err); constraint != "stripe_events_id_check" {
		t.Errorf("empty event id: err = %v, want stripe_events_id_check", err)
	}

	insertSub := `INSERT INTO stripe_subscriptions (id, user_id, customer_id, status, paid, event_id, event_created_at)
VALUES ($1, $2, $3, $4, true, 'evt_m1', now())`
	if _, err := pool.Exec(ctx, insertSub, "sub_m1", userID, "cus_m1", "active"); err != nil {
		t.Fatalf("subscription: %v", err)
	}
	for name, tc := range map[string]struct {
		id, customer, status, constraint string
	}{
		"same id":        {"sub_m1", "cus_m1", "active", "stripe_subscriptions_pkey"},
		"empty id":       {"", "cus_m1", "active", "stripe_subscriptions_id_check"},
		"empty customer": {"sub_m2", "", "active", "stripe_subscriptions_customer_id_check"},
		"empty status":   {"sub_m3", "cus_m1", "", "stripe_subscriptions_status_check"},
	} {
		_, err := pool.Exec(ctx, insertSub, tc.id, userID, tc.customer, tc.status)
		if _, constraint := pgCode(err); constraint != tc.constraint {
			t.Errorf("%s: err = %v, want %s", name, err, tc.constraint)
		}
	}
	_, err = pool.Exec(ctx, insertSub, "sub_m4", "00000000-0000-0000-0000-000000000000", "cus_m1", "active")
	if _, constraint := pgCode(err); constraint != "stripe_subscriptions_user_id_fkey" {
		t.Errorf("unknown user: err = %v, want stripe_subscriptions_user_id_fkey", err)
	}
	if !tableExists(t, pool, schema, "stripe_subscriptions_user_id_idx") {
		t.Error("stripe_subscriptions_user_id_idx missing")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stripe_subscriptions WHERE id = 'sub_m1'`).Scan(&left); err != nil || left != 0 {
		t.Errorf("subscriptions left after deleting their user = %d, %v", left, err)
	}
}

func TestStripePlanSyncMigrationRollsBackAndReapplies(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.UpTo(ctx, 15); err != nil {
		t.Fatalf("UpTo(15): %v", err)
	}
	if !columnExists(t, pool, schema, "users", "plan") || !tableExists(t, pool, schema, "stripe_events") || !tableExists(t, pool, schema, "stripe_subscriptions") {
		t.Fatal("plan, stripe_events or stripe_subscriptions missing after Up")
	}

	if _, err := provider.DownTo(ctx, 14); err != nil {
		t.Fatalf("DownTo(14): %v", err)
	}
	if columnExists(t, pool, schema, "users", "plan") {
		t.Error("users.plan still exists after rolling back 00015")
	}
	for _, table := range []string{"stripe_events", "stripe_subscriptions"} {
		if tableExists(t, pool, schema, table) {
			t.Errorf("%s still exists after rolling back 00015", table)
		}
	}
	if !columnExists(t, pool, schema, "users", "stripe_customer_id") || !tableExists(t, pool, schema, "agency_submissions") {
		t.Error("an earlier migration's structures went with 00015")
	}

	if _, err := provider.UpTo(ctx, 15); err != nil {
		t.Fatalf("UpTo(15) after Down: %v", err)
	}
	if !columnExists(t, pool, schema, "users", "plan") || !tableExists(t, pool, schema, "stripe_events") || !tableExists(t, pool, schema, "stripe_subscriptions") {
		t.Error("00015's structures missing after reapplying")
	}
}
