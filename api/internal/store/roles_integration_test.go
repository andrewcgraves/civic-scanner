//go:build integration

package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func uniqueEmail() string {
	return "role_" + uuid.NewString() + "@example.com"
}

func newUser(t *testing.T, pool *pgxpool.Pool, email string) store.User {
	t.Helper()
	u, created, err := store.New(pool).EnsureUser(t.Context(), store.UserInput{WorkOSUserID: workOSUserID(t, pool), Email: email})
	if err != nil || !created {
		t.Fatalf("EnsureUser = (%+v, created %v, err %v)", u, created, err)
	}
	return u
}

func TestNewUsersAreNotAdmins(t *testing.T) {
	u := newUser(t, testdb.Pool(t), uniqueEmail())
	if u.Role != store.RoleUser || u.Role.IsAdmin() {
		t.Errorf("new user role = %q, want %q", u.Role, store.RoleUser)
	}
}

func TestSetUserRolePromotesAndDemotes(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	email := uniqueEmail()
	u := newUser(t, pool, email)

	promoted, err := s.SetUserRole(ctx, store.UserRef{ID: u.ID}, store.RoleAdmin, time.Second)
	if err != nil || promoted.Previous != store.RoleUser || promoted.User.Role != store.RoleAdmin || promoted.User.ID != u.ID || !promoted.User.UpdatedAt.After(u.UpdatedAt) {
		t.Fatalf("promote = (%+v, %v)", promoted, err)
	}
	seen, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: u.WorkOSUserID, Email: email})
	if err != nil || seen.Role != store.RoleAdmin {
		t.Fatalf("EnsureUser after promotion = (%+v, %v), want the admin role", seen, err)
	}

	again, err := s.SetUserRole(ctx, store.UserRef{Email: strings.ToUpper(email)}, store.RoleAdmin, time.Second)
	if err != nil || again.Previous != store.RoleAdmin || again.User.Role != store.RoleAdmin || !again.User.UpdatedAt.Equal(promoted.User.UpdatedAt) {
		t.Fatalf("promote again by email = (%+v, %v), want no change", again, err)
	}

	demoted, err := s.SetUserRole(ctx, store.UserRef{Email: email}, store.RoleUser, time.Second)
	if err != nil || demoted.Previous != store.RoleAdmin || demoted.User.Role != store.RoleUser {
		t.Fatalf("demote = (%+v, %v)", demoted, err)
	}
	seen, _, err = s.EnsureUser(ctx, store.UserInput{WorkOSUserID: u.WorkOSUserID})
	if err != nil || seen.Role != store.RoleUser {
		t.Fatalf("EnsureUser after demotion = (%+v, %v), want the user role", seen, err)
	}
}

func TestSetUserRoleRefusals(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	shared := uniqueEmail()
	a := newUser(t, pool, shared)
	b := newUser(t, pool, shared)

	_, err := s.SetUserRole(ctx, store.UserRef{Email: shared}, store.RoleAdmin, time.Second)
	if !errors.Is(err, store.ErrAmbiguousUser) || !strings.Contains(err.Error(), a.ID.String()) || !strings.Contains(err.Error(), b.ID.String()) {
		t.Errorf("shared email: err = %v, want ErrAmbiguousUser naming both ids", err)
	}

	cases := map[string]struct {
		ref  store.UserRef
		role store.Role
		want error
	}{
		"unknown id":         {store.UserRef{ID: uuid.New()}, store.RoleAdmin, store.ErrNotFound},
		"unknown email":      {store.UserRef{Email: uniqueEmail()}, store.RoleAdmin, store.ErrNotFound},
		"unknown role":       {store.UserRef{ID: a.ID}, "superuser", store.ErrInvalidRole},
		"empty role":         {store.UserRef{ID: a.ID}, "", store.ErrInvalidRole},
		"no reference":       {store.UserRef{}, store.RoleAdmin, store.ErrInvalidUser},
		"id and email given": {store.UserRef{ID: a.ID, Email: shared}, store.RoleAdmin, store.ErrInvalidUser},
	}
	for name, tc := range cases {
		if _, err := s.SetUserRole(ctx, tc.ref, tc.role, time.Second); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}

	for _, u := range []store.User{a, b} {
		var role string
		if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, u.ID).Scan(&role); err != nil || role != "user" {
			t.Errorf("user %s role = %q, %v; want user, unchanged", u.ID, role, err)
		}
	}

	if _, err := s.SetUserRole(ctx, store.UserRef{ID: a.ID}, store.RoleAdmin, 0); err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Errorf("zero lock timeout: err = %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.SetUserRole(cancelled, store.UserRef{ID: a.ID}, store.RoleAdmin, time.Second); err == nil {
		t.Error("SetUserRole with a cancelled context succeeded")
	}
}

func TestEmailChangesNeverTouchTheRole(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	u := newUser(t, pool, uniqueEmail())
	if _, err := s.SetUserRole(ctx, store.UserRef{ID: u.ID}, store.RoleAdmin, time.Second); err != nil {
		t.Fatal(err)
	}

	for _, email := range []string{uniqueEmail(), "", uniqueEmail()} {
		seen, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: u.WorkOSUserID, Email: email})
		if err != nil || created || seen.ID != u.ID || seen.Role != store.RoleAdmin {
			t.Fatalf("EnsureUser with email %q = (%+v, created %v, err %v), want the same admin", email, seen, created, err)
		}
		var role string
		if err := pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, u.ID).Scan(&role); err != nil || role != "admin" {
			t.Fatalf("stored role after email %q = %q, %v; want admin", email, role, err)
		}
	}

	if _, err := s.SetUserRole(ctx, store.UserRef{ID: u.ID}, store.RoleUser, time.Second); err != nil {
		t.Fatal(err)
	}
	seen, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: u.WorkOSUserID, Email: uniqueEmail()})
	if err != nil || seen.Role != store.RoleUser {
		t.Fatalf("EnsureUser with a new email after demotion = (%+v, %v), want role user", seen, err)
	}
}

func TestSetUserRoleGivesUpOnALockedRow(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	u := newUser(t, pool, uniqueEmail())

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.WithoutCancel(ctx)) })
	if _, err := holder.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, u.ID); err != nil {
		t.Fatal(err)
	}

	_, err = s.SetUserRole(ctx, store.UserRef{ID: u.ID}, store.RoleAdmin, 50*time.Millisecond)
	if !errors.Is(err, store.ErrUserLocked) {
		t.Fatalf("err = %v, want ErrUserLocked", err)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	change, err := s.SetUserRole(ctx, store.UserRef{ID: u.ID}, store.RoleAdmin, time.Second)
	if err != nil || change.User.Role != store.RoleAdmin {
		t.Fatalf("after the lock is released = (%+v, %v)", change, err)
	}
}
