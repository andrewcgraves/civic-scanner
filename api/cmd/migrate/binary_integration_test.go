//go:build integration

package main

import (
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestBinaryUp(t *testing.T) {
	cmd := testbin.Command(t, testbin.Build(t), []string{"DATABASE_URL=" + testdb.URL(t), "LOG_LEVEL=error"}, "up")
	if out, err := cmd.CombinedOutput(); testbin.ExitCode(t, err) != 0 {
		t.Fatalf("migrate up: %v\n%s", err, out)
	}
}
