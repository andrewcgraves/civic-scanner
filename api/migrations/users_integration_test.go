//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestUsersDefaultsAndConstraints(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id LIKE 'user_migration_test_%'`)
	})

	var (
		email                  *string
		idSet, created, update bool
	)
	if err := pool.QueryRow(ctx, `
INSERT INTO users (workos_user_id) VALUES ('user_migration_test_defaults')
RETURNING email, id IS NOT NULL, created_at IS NOT NULL, updated_at IS NOT NULL`).Scan(&email, &idSet, &created, &update); err != nil {
		t.Fatal(err)
	}
	if email != nil || !idSet || !created || !update {
		t.Errorf("email = %v, id set = %v, timestamps set = %v %v", email, idSet, created, update)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_migration_test_defaults')`); violatedConstraint(err) != "users_workos_user_id_unique" {
		t.Errorf("duplicate WorkOS user id: err = %v, want users_workos_user_id_unique", err)
	}
	for name, tc := range map[string]struct {
		workosID   string
		email      any
		constraint string
	}{
		"empty WorkOS user id": {"", nil, "users_workos_user_id_check"},
		"empty email":          {"user_migration_test_empty_email", "", "users_email_check"},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO users (workos_user_id, email) VALUES ($1, $2)`, tc.workosID, tc.email)
		if got := violatedConstraint(err); got != tc.constraint {
			t.Errorf("%s: err = %v, want a violation of %s", name, err, tc.constraint)
		}
	}

	for _, id := range []string{"user_migration_test_shared_a", "user_migration_test_shared_b"} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (workos_user_id, email) VALUES ($1, 'shared@example.com')`, id); err != nil {
			t.Errorf("same email for %s rejected: %v", id, err)
		}
	}
}
