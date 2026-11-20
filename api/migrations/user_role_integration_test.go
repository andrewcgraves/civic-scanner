//go:build integration

package migrations

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

func pgCode(err error) (code, constraint string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName
	}
	return "", ""
}

func TestUserRoleMigration(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.UpTo(ctx, 11); err != nil {
		t.Fatalf("UpTo(11): %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_before_roles')`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 12); err != nil {
		t.Fatalf("UpTo(12): %v", err)
	}

	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE workos_user_id = 'user_before_roles'`).Scan(&role); err != nil || role != "user" {
		t.Errorf("existing user role = %q, %v; want user", role, err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_after_roles') RETURNING role`).Scan(&role); err != nil || role != "user" {
		t.Errorf("new user role = %q, %v; want user", role, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE workos_user_id = 'user_after_roles'`); err != nil {
		t.Errorf("admin role rejected: %v", err)
	}
	for value, want := range map[any][2]string{
		"superuser": {"23514", "users_role_check"},
		"Admin":     {"23514", "users_role_check"},
		"":          {"23514", "users_role_check"},
		nil:         {"23502", ""},
	} {
		_, err := pool.Exec(ctx, `UPDATE users SET role = $1 WHERE workos_user_id = 'user_after_roles'`, value)
		if code, constraint := pgCode(err); code != want[0] || constraint != want[1] {
			t.Errorf("role %v: err = %v, want code %s on %q", value, err, want[0], want[1])
		}
	}

	if _, err := provider.DownTo(ctx, 11); err != nil {
		t.Fatalf("DownTo(11): %v", err)
	}
	if columnExists(t, pool, schema, "users", "role") {
		t.Error("users.role still exists after rolling back 00012")
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE workos_user_id = 'user_after_roles'`).Scan(&role); err != nil || role != "user" {
		t.Errorf("role after reapplying = %q, %v; want user", role, err)
	}
}

func TestStoreTreatsAnUnknownRoleAsNotAdmin(t *testing.T) {
	ctx := t.Context()
	provider, pool, _ := isolatedProvider(t)
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE users DROP CONSTRAINT users_role_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (workos_user_id, role) VALUES ('user_future_role', 'moderator')`); err != nil {
		t.Fatal(err)
	}

	s := store.New(pool)
	u, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: "user_future_role"})
	if err != nil || created {
		t.Fatalf("EnsureUser = (%+v, created %v, err %v)", u, created, err)
	}
	if u.Role != "moderator" || u.Role.IsAdmin() {
		t.Errorf("role = %q, admin %v; want moderator, not admin", u.Role, u.Role.IsAdmin())
	}

	change, err := s.SetUserRole(ctx, store.UserRef{ID: u.ID}, store.RoleUser)
	if err != nil || change.Previous != "moderator" || change.User.Role != store.RoleUser {
		t.Errorf("SetUserRole from an unknown role = (%+v, %v)", change, err)
	}
}
