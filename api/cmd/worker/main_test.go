package main

import (
	"strings"
	"testing"
)

const unreachableDatabaseURL = "postgres://127.0.0.1:1/civic?sslmode=disable&connect_timeout=1"

func TestRunRequiresConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run(t.Context()); err == nil {
		t.Fatal("expected an error when DATABASE_URL is unset")
	}
}

func TestRunInitialisesFeatureFlags(t *testing.T) {
	t.Setenv("DATABASE_URL", unreachableDatabaseURL)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("POSTHOG_API_KEY", "phc_test")
	t.Setenv("POSTHOG_HOST", "://not-a-url")
	err := run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "feature flags") {
		t.Fatalf("run() = %v, want a feature flags error", err)
	}
}

func TestRunFailsWhenDatabaseUnreachable(t *testing.T) {
	t.Setenv("DATABASE_URL", unreachableDatabaseURL)
	t.Setenv("LOG_LEVEL", "error")
	if err := run(t.Context()); err == nil {
		t.Fatal("expected an error when the database is unreachable")
	}
}
