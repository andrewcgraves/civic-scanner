package main

import "testing"

const unreachableDatabaseURL = "postgres://127.0.0.1:1/civic?sslmode=disable&connect_timeout=1"

func TestRunRequiresUpArgument(t *testing.T) {
	for _, args := range [][]string{nil, {"down"}, {"up", "extra"}} {
		if err := run(t.Context(), args); err == nil || err.Error() != "usage: migrate up" {
			t.Errorf("run(%q) = %v, want usage error", args, err)
		}
	}
}

func TestRunRequiresConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run(t.Context(), []string{"up"}); err == nil {
		t.Fatal("expected an error when DATABASE_URL is unset")
	}
}

func TestRunFailsWhenDatabaseUnreachable(t *testing.T) {
	t.Setenv("DATABASE_URL", unreachableDatabaseURL)
	t.Setenv("LOG_LEVEL", "error")
	if err := run(t.Context(), []string{"up"}); err == nil {
		t.Fatal("expected an error when the database is unreachable")
	}
}
