//go:build integration

package migrations

import "testing"

func TestUserStripeCustomerMigrationRollsBackAndReapplies(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !columnExists(t, pool, schema, "users", "stripe_customer_id") || !tableExists(t, pool, schema, "users_stripe_customer_id_unique") {
		t.Fatal("stripe_customer_id or its index missing after Up")
	}

	if _, err := provider.DownTo(ctx, 11); err != nil {
		t.Fatalf("DownTo(11): %v", err)
	}
	if columnExists(t, pool, schema, "users", "stripe_customer_id") {
		t.Error("stripe_customer_id still exists after rolling back 00013")
	}
	if tableExists(t, pool, schema, "users_stripe_customer_id_unique") {
		t.Error("users_stripe_customer_id_unique still exists after rolling back 00013")
	}
	if !columnExists(t, pool, schema, "agenda_versions", "change_event_pending") {
		t.Error("change_event_pending missing after rolling back only 00013")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if !columnExists(t, pool, schema, "users", "stripe_customer_id") || !tableExists(t, pool, schema, "users_stripe_customer_id_unique") {
		t.Error("stripe_customer_id or its index missing after reapplying")
	}
}
