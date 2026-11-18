//go:build integration

package main

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestBinaryStopsOnSIGTERM(t *testing.T) {
	testdb.Pool(t)
	cmd := testbin.Command(t, testbin.Build(t), []string{"DATABASE_URL=" + testdb.URL(t), "LOG_LEVEL=info"})
	started := testbin.WatchStdout(cmd, `"msg":"worker started"`)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	done := testbin.Start(t, cmd)

	select {
	case <-started:
	case err := <-done:
		t.Fatalf("worker exited before starting: %v\n%s", err, stderr.String())
	case <-time.After(15 * time.Second):
		t.Fatal("worker did not log that it started within 15s")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := testbin.ExitCode(t, <-done); code != 0 {
		t.Errorf("exit code = %d, want 0\n%s", code, stderr.String())
	}
}
