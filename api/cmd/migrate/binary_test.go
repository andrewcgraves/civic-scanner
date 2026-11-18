package main

import (
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
)

func TestBinaryExitsWithUsageError(t *testing.T) {
	cmd := testbin.Command(t, testbin.Build(t), nil)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if code := testbin.ExitCode(t, cmd.Run()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "migrate: usage: migrate up\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}
