//go:build integration

package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func setupEnv(t *testing.T, port int) {
	t.Helper()
	t.Setenv("DATABASE_URL", testdb.URL(t))
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("PORT", strconv.Itoa(port))
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", ":0") //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func waitForRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return within 30s")
		return nil
	}
}

func healthy(url string) bool {
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestRunServesUntilCanceled(t *testing.T) {
	l := listen(t)
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	setupEnv(t, port)
	t.Setenv("PUBLIC_WEB_URL", "https://civic.example")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_FAKE")
	t.Setenv("STRIPE_PRICE_ID", "price_FAKE")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/v1/healthz"
	deadline := time.Now().Add(15 * time.Second)
	for !healthy(url) {
		select {
		case err := <-done:
			t.Fatalf("run returned before serving: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s did not return 200 within 15s", url)
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	if err := waitForRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunFailsWhenPortInUse(t *testing.T) {
	l := listen(t)
	t.Cleanup(func() { _ = l.Close() })
	setupEnv(t, l.Addr().(*net.TCPAddr).Port)

	done := make(chan error, 1)
	go func() { done <- run(t.Context()) }()

	if err := waitForRun(t, done); err == nil {
		t.Fatal("expected an error when the port is in use")
	}
}
