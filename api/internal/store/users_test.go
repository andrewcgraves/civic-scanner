package store

import (
	"errors"
	"testing"
)

func TestParseRole(t *testing.T) {
	for _, s := range []string{"user", "admin"} {
		if r, err := ParseRole(s); err != nil || string(r) != s {
			t.Errorf("ParseRole(%q) = %q, %v", s, r, err)
		}
	}
	for _, s := range []string{"", "Admin", "ADMIN", " admin", "superuser", "moderator"} {
		if r, err := ParseRole(s); !errors.Is(err, ErrInvalidRole) || r != "" {
			t.Errorf("ParseRole(%q) = %q, %v; want ErrInvalidRole", s, r, err)
		}
	}
}

func TestOnlyTheAdminRoleIsAdmin(t *testing.T) {
	for role, want := range map[Role]bool{RoleAdmin: true, RoleUser: false, "": false, "Admin": false, "superuser": false} {
		if got := role.IsAdmin(); got != want {
			t.Errorf("Role(%q).IsAdmin() = %v, want %v", role, got, want)
		}
	}
}
