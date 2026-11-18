//go:build integration

package main

import (
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestBinaryServesAndStopsOnSIGTERM(t *testing.T) {
	bin := testbin.Build(t)
	l := listen(t)
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := testbin.Command(t, bin, []string{"DATABASE_URL=" + testdb.URL(t), "LOG_LEVEL=error", "PORT=" + port})
	var stderr strings.Builder
	cmd.Stderr = &stderr
	done := testbin.Start(t, cmd)

	url := "http://127.0.0.1:" + port + "/v1/healthz"
	deadline := time.Now().Add(15 * time.Second)
	for !healthy(url) {
		select {
		case err := <-done:
			t.Fatalf("api exited before serving: %v\n%s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s did not return 200 within 15s", url)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := testbin.ExitCode(t, <-done); code != 0 {
		t.Errorf("exit code = %d, want 0\n%s", code, stderr.String())
	}
}
