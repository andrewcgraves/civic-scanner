package main

import (
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
)

func TestBinaryExitsWithErrorOnBadConfig(t *testing.T) {
	cmd := testbin.Command(t, testbin.Build(t), []string{"DATABASE_URL="})
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if code := testbin.ExitCode(t, cmd.Run()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "worker: DATABASE_URL is required\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}
