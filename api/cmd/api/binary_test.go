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
	if want := "api: DATABASE_URL is required\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestBinaryLogsInvalidBillingOnceAndKeepsGoing(t *testing.T) {
	cmd := testbin.Command(t, testbin.Build(t), []string{
		"DATABASE_URL=postgres://127.0.0.1:1/civic?sslmode=disable&connect_timeout=1",
		"PUBLIC_WEB_URL=http://civic.example",
		"STRIPE_SECRET_KEY=sk_live_FAKEFAKE",
		"STRIPE_PRICE_ID=price_FAKE",
	})
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if code := testbin.ExitCode(t, cmd.Run()); code != 1 {
		t.Errorf("exit code = %d, want 1 from the unreachable database", code)
	}
	if !strings.Contains(stderr.String(), "database") {
		t.Errorf("stderr = %q, want the database error", stderr.String())
	}
	logs := stdout.String()
	if strings.Count(logs, "billing is not configured") != 1 || !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, "PUBLIC_WEB_URL must be an https URL") {
		t.Errorf("logs = %s, want one error naming PUBLIC_WEB_URL", logs)
	}
	if strings.Contains(logs+stderr.String(), "FAKEFAKE") {
		t.Errorf("the key was printed: %s %s", logs, stderr.String())
	}
}
