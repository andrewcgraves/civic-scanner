//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestUserStripeCustomerIsOptionalUniqueAndNotEmpty(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id LIKE 'user_migration_stripe_%'`)
	})

	var valid, unique bool
	if err := pool.QueryRow(ctx, `
SELECT i.indisvalid, i.indisunique FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'users_stripe_customer_id_unique' AND i.indrelid = 'users'::regclass`).Scan(&valid, &unique); err != nil {
		t.Fatalf("users_stripe_customer_id_unique: %v", err)
	}
	if !valid || !unique {
		t.Errorf("users_stripe_customer_id_unique valid = %v, unique = %v, want both", valid, unique)
	}

	var customer *string
	if err := pool.QueryRow(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_migration_stripe_a') RETURNING stripe_customer_id`).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if customer != nil {
		t.Errorf("stripe_customer_id = %q, want null by default", *customer)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_migration_stripe_b'), ('user_migration_stripe_c')`); err != nil {
		t.Fatalf("several users without a customer: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE users SET stripe_customer_id = 'cus_migration_shared' WHERE workos_user_id = 'user_migration_stripe_a'`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `UPDATE users SET stripe_customer_id = 'cus_migration_shared' WHERE workos_user_id = 'user_migration_stripe_b'`)
	if got := violatedConstraint(err); got != "users_stripe_customer_id_unique" {
		t.Errorf("shared customer: err = %v, want users_stripe_customer_id_unique", err)
	}
	_, err = pool.Exec(ctx, `UPDATE users SET stripe_customer_id = '' WHERE workos_user_id = 'user_migration_stripe_c'`)
	if got := violatedConstraint(err); got != "users_stripe_customer_id_check" {
		t.Errorf("empty customer: err = %v, want users_stripe_customer_id_check", err)
	}
}
