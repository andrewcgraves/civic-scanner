//go:build integration

package main

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func signedInUser(t *testing.T, pool *pgxpool.Pool, email string) store.User {
	t.Helper()
	workosID := "user_set_role_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE workos_user_id = $1`, workosID)
	})
	u, _, err := store.New(pool).EnsureUser(t.Context(), store.UserInput{WorkOSUserID: workosID, Email: email})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func runSetRole(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := setRole(t.Context(), args, &out)
	return out.String(), err
}

func storedRole(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var role string
	if err := pool.QueryRow(t.Context(), `SELECT role FROM users WHERE id = $1`, id).Scan(&role); err != nil {
		t.Fatal(err)
	}
	return role
}

func TestSetRoleCommandPromotesAndDemotes(t *testing.T) {
	t.Setenv("DATABASE_URL", testdb.URL(t))
	t.Setenv("LOG_LEVEL", "error")
	pool := testdb.Pool(t)
	email := "set_role_" + uuid.NewString() + "@example.com"
	u := signedInUser(t, pool, email)

	out, err := runSetRole(t, strings.ToUpper(email), "admin")
	if err != nil || !strings.HasPrefix(out, "user "+u.ID.String()+": role is now admin (was user)\n  email "+email+", WorkOS user "+u.WorkOSUserID+", first seen ") {
		t.Fatalf("promote: output %q, err %v", out, err)
	}
	if got := storedRole(t, pool, u.ID); got != "admin" {
		t.Errorf("role after promote = %q, want admin", got)
	}

	if out, err := runSetRole(t, u.ID.String(), "admin"); err != nil || !strings.Contains(out, "role was already admin; nothing changed") {
		t.Errorf("promote again: output %q, err %v", out, err)
	}

	if out, err := runSetRole(t, u.ID.String(), "user"); err != nil || !strings.Contains(out, "role is now user (was admin)") {
		t.Errorf("demote: output %q, err %v", out, err)
	}
	if got := storedRole(t, pool, u.ID); got != "user" {
		t.Errorf("role after demote = %q, want user", got)
	}
}

func TestSetRoleCommandRefusals(t *testing.T) {
	t.Setenv("DATABASE_URL", testdb.URL(t))
	t.Setenv("LOG_LEVEL", "error")
	pool := testdb.Pool(t)
	shared := "set_role_shared_" + uuid.NewString() + "@example.com"
	a := signedInUser(t, pool, shared)
	b := signedInUser(t, pool, shared)

	out, err := runSetRole(t, shared, "admin")
	if !errors.Is(err, store.ErrAmbiguousUser) || !errors.Is(err, errAmbiguousUserHint) || !strings.Contains(err.Error(), a.ID.String()) || !strings.Contains(err.Error(), b.ID.String()) || out != "" {
		t.Errorf("shared email: output %q, err %v", out, err)
	}
	if out, err := runSetRole(t, uuid.NewString(), "admin"); !errors.Is(err, errUserNotFound) || out != "" {
		t.Errorf("unknown id: output %q, err %v", out, err)
	}
	if out, err := runSetRole(t, "nobody_"+uuid.NewString()+"@example.com", "admin"); !errors.Is(err, errUserNotFound) || out != "" {
		t.Errorf("unknown email: output %q, err %v", out, err)
	}
	for _, u := range []store.User{a, b} {
		if got := storedRole(t, pool, u.ID); got != "user" {
			t.Errorf("user %s role = %q, want user, unchanged", u.ID, got)
		}
	}
}

func TestSetRoleCommandNeedsTheMigration(t *testing.T) {
	pool := testdb.Pool(t)
	schema := "set_role_empty_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := pool.Exec(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DROP SCHEMA `+schema+` CASCADE`)
	})
	u, err := url.Parse(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	t.Setenv("DATABASE_URL", u.String())
	t.Setenv("LOG_LEVEL", "error")

	if out, err := runSetRole(t, uuid.NewString(), "admin"); !errors.Is(err, errRoleNotMigrated) || out != "" {
		t.Errorf("output %q, err %v; want errRoleNotMigrated", out, err)
	}
}

func TestSetRoleCommandGivesUpOnALockedUser(t *testing.T) {
	pool := testdb.Pool(t)
	u := signedInUser(t, pool, "set_role_locked_"+uuid.NewString()+"@example.com")
	holder, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.WithoutCancel(t.Context())) })
	if _, err := holder.Exec(t.Context(), `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, u.ID); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	err = applyRole(t.Context(), pool, roleTarget{ref: store.UserRef{ID: u.ID}, role: store.RoleAdmin}, 50*time.Millisecond, &out)
	if !errors.Is(err, errUserBusy) || !errors.Is(err, store.ErrUserLocked) || out.String() != "" {
		t.Fatalf("output %q, err %v; want errUserBusy", out.String(), err)
	}
	if !strings.HasPrefix(err.Error(), "another session is changing this user right now; nothing changed; try again") {
		t.Errorf("message = %q", err.Error())
	}
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := storedRole(t, pool, u.ID); got != "user" {
		t.Errorf("role = %q, want user, unchanged", got)
	}
}

func TestSetRoleCommandReportsOtherDatabaseErrors(t *testing.T) {
	pool := testdb.Pool(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := applyRole(ctx, pool, roleTarget{ref: store.UserRef{ID: uuid.New()}, role: store.RoleAdmin}, time.Second, io.Discard)
	if err == nil || !strings.HasSuffix(err.Error(), "; nothing changed") {
		t.Errorf("err = %v, want a database error ending in nothing changed", err)
	}
}
