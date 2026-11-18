//go:build integration

package main

import (
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestRunUp(t *testing.T) {
	t.Setenv("DATABASE_URL", testdb.URL(t))
	t.Setenv("LOG_LEVEL", "error")

	if err := run(t.Context(), []string{"up"}); err != nil {
		t.Fatalf("run: %v", err)
	}
}
