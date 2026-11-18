package store

import (
	"strings"
	"testing"
)

func TestOpenRejectsInvalidURL(t *testing.T) {
	_, err := Open(t.Context(), "postgres://%zz")
	if err == nil || !strings.HasPrefix(err.Error(), "open pool:") {
		t.Fatalf("err = %v, want open pool error", err)
	}
}

func TestOpenFailsWhenDatabaseUnreachable(t *testing.T) {
	_, err := Open(t.Context(), "postgres://127.0.0.1:1/civic?sslmode=disable&connect_timeout=1")
	if err == nil || !strings.HasPrefix(err.Error(), "ping database:") {
		t.Fatalf("err = %v, want ping database error", err)
	}
}
