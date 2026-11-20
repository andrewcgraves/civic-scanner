package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

func TestParseSetRoleArgs(t *testing.T) {
	id := uuid.MustParse("3f1c2d4e-5a6b-4c7d-8e9f-0a1b2c3d4e5f")
	cases := []struct {
		args []string
		want roleTarget
		err  bool
	}{
		{args: []string{id.String(), "admin"}, want: roleTarget{ref: store.UserRef{ID: id}, role: store.RoleAdmin}},
		{args: []string{" " + id.String() + " ", "user"}, want: roleTarget{ref: store.UserRef{ID: id}, role: store.RoleUser}},
		{args: []string{"Ada@Example.com", "admin"}, want: roleTarget{ref: store.UserRef{Email: "Ada@Example.com"}, role: store.RoleAdmin}},
		{args: []string{"--help"}, want: roleTarget{help: true}},
		{args: nil, err: true},
		{args: []string{id.String()}, err: true},
		{args: []string{id.String(), "admin", "extra"}, err: true},
		{args: []string{id.String(), "Admin"}, err: true},
		{args: []string{id.String(), "superuser"}, err: true},
		{args: []string{id.String(), ""}, err: true},
		{args: []string{uuid.Nil.String(), "admin"}, err: true},
		{args: []string{"user_01ABC", "admin"}, err: true},
		{args: []string{"", "admin"}, err: true},
		{args: []string{"--email=ada@example.com", "admin"}, err: true},
	}
	for _, tc := range cases {
		got, err := parseSetRoleArgs(tc.args)
		if tc.err {
			if !errors.Is(err, errSetRoleUsage) {
				t.Errorf("parseSetRoleArgs(%q) error = %v, want the usage error", tc.args, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseSetRoleArgs(%q) = %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
}

func TestReportRoleChange(t *testing.T) {
	email := "ada@example.com"
	id := uuid.MustParse("3f1c2d4e-5a6b-4c7d-8e9f-0a1b2c3d4e5f")
	created := time.Date(2026, 11, 18, 4, 0, 0, 0, time.FixedZone("PST", -8*3600))
	user := store.User{ID: id, WorkOSUserID: "user_01", Email: &email, Role: store.RoleAdmin, CreatedAt: created}

	cases := map[string]struct {
		change store.RoleChange
		want   string
	}{
		"promoted": {
			store.RoleChange{User: user, Previous: store.RoleUser},
			"user " + id.String() + ": role is now admin (was user)\n  email ada@example.com, WorkOS user user_01, first seen 2026-11-18T12:00:00Z\n",
		},
		"unchanged without email": {
			store.RoleChange{User: store.User{ID: id, WorkOSUserID: "user_01", Role: store.RoleUser, CreatedAt: created}, Previous: store.RoleUser},
			"user " + id.String() + ": role was already user; nothing changed\n  email none, WorkOS user user_01, first seen 2026-11-18T12:00:00Z\n",
		},
	}
	for name, tc := range cases {
		var out strings.Builder
		if err := reportRoleChange(&out, tc.change); err != nil || out.String() != tc.want {
			t.Errorf("%s: output = %q, err %v; want %q", name, out.String(), err, tc.want)
		}
	}
}

func TestSetRoleFailsWhenDatabaseUnreachable(t *testing.T) {
	t.Setenv("DATABASE_URL", unreachableDatabaseURL)
	err := dispatch(t.Context(), []string{setRoleCommand, "ada@example.com", "admin"}, io.Discard, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "could not connect to the database (connect timeout 1s); nothing changed: ") {
		t.Errorf("error = %v, want a connect failure saying nothing changed", err)
	}
}
