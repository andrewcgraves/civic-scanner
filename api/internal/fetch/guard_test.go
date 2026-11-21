package fetch_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

func capturedLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}

func assertRefused(t *testing.T, err error, host string) {
	t.Helper()
	if !errors.Is(err, fetch.ErrForbiddenAddress) {
		t.Fatalf("err = %v, want ErrForbiddenAddress", err)
	}
	if fetch.IsRetryable(err) {
		t.Errorf("err = %v, want permanent", err)
	}
	var fe *fetch.Error
	if !errors.As(err, &fe) {
		t.Errorf("err = %T, want *fetch.Error", err)
	}
	if !strings.Contains(err.Error(), host) {
		t.Errorf("err = %q, want it to name %s", err, host)
	}
}

func TestPlainRefusesNonPublicAddresses(t *testing.T) {
	hosts := map[string]string{
		"loopback":                 "127.0.0.1",
		"loopback range":           "127.8.9.10",
		"ipv6 loopback":            "[::1]",
		"rfc1918 10/8":             "10.1.2.3",
		"rfc1918 172.16/12":        "172.16.0.1",
		"rfc1918 192.168/16":       "192.168.1.1",
		"link-local":               "169.254.1.1",
		"cloud metadata":           "169.254.169.254",
		"ipv6 link-local":          "[fe80::1]",
		"carrier-grade nat":        "100.64.0.1",
		"unique-local":             "[fd12:3456::1]",
		"unspecified":              "0.0.0.0",
		"ipv6 unspecified":         "[::]",
		"ipv4-mapped loopback":     "[::ffff:127.0.0.1]",
		"ipv4-mapped private":      "[::ffff:10.0.0.1]",
		"ipv4-mapped metadata":     "[::ffff:169.254.169.254]",
		"ipv4-compatible loopback": "[::127.0.0.1]",
	}
	for name, host := range hosts {
		t.Run(name, func(t *testing.T) {
			logger, logs := capturedLogger()
			_, err := fetch.NewPlain(plainConfig(), fetch.WithLogger(logger)).Fetch(t.Context(), fetch.Request{URL: "http://" + host + "/latest/meta-data/"})
			assertRefused(t, err, strings.Trim(host, "[]"))
			if n := strings.Count(logs.String(), "fetch refused a non-public address"); n != 1 {
				t.Errorf("logged %d refusals, want 1:\n%s", n, logs)
			}
		})
	}
}

func TestPlainRefusesAHostnameThatResolvesToAPrivateAddress(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	local := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)

	logger, logs := capturedLogger()
	_, err := fetch.NewPlain(plainConfig(), fetch.WithLogger(logger)).Fetch(t.Context(), fetch.Request{URL: local + "/agenda.pdf"})
	assertRefused(t, err, "localhost")
	if hits.Load() != 0 {
		t.Error("the local server was reached")
	}
	if !strings.Contains(logs.String(), "host=localhost") {
		t.Errorf("log does not name the host:\n%s", logs)
	}
}

func TestPlainRefusesARedirectToAPrivateAddress(t *testing.T) {
	origin := netip.MustParseAddr("127.0.0.1")
	onlyOrigin := fetch.AllowAddress(func(addr netip.Addr) bool { return addr == origin })
	targets := map[string]string{
		"metadata literal": "http://169.254.169.254/latest/meta-data/",
		"private literal":  "https://10.0.0.5/admin",
		"other loopback":   "http://127.0.0.2:5432/",
		"mapped ipv6":      "http://[::ffff:192.168.0.1]/",
	}
	for name, target := range targets {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target, http.StatusFound)
			}))
			defer srv.Close()

			logger, logs := capturedLogger()
			_, err := fetch.NewPlain(plainConfig(), onlyOrigin, fetch.WithLogger(logger)).Fetch(t.Context(), fetch.Request{URL: srv.URL + "/agenda"})
			if !errors.Is(err, fetch.ErrForbiddenAddress) || fetch.IsRetryable(err) {
				t.Fatalf("err = %v, want a permanent ErrForbiddenAddress", err)
			}
			if n := strings.Count(logs.String(), "fetch refused a non-public address"); n != 1 {
				t.Errorf("logged %d refusals, want 1:\n%s", n, logs)
			}
		})
	}
}

func TestPlainFollowsOnlyHTTPRedirects(t *testing.T) {
	for _, target := range []string{"file:///etc/passwd", "ftp://agency.test/agenda.pdf", "gopher://agency.test/", "javascript:alert(1)", "data:text/html,hi"} {
		t.Run(target, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", target)
				w.WriteHeader(http.StatusFound)
			}))
			defer srv.Close()

			_, err := localPlain(plainConfig()).Fetch(t.Context(), fetch.Request{URL: srv.URL})
			if !errors.Is(err, fetch.ErrInvalidURL) || fetch.IsRetryable(err) {
				t.Errorf("err = %v, want a permanent ErrInvalidURL", err)
			}
		})
	}
}

func TestPlainDefaultRefusesLocalTestServers(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	_, err := fetch.NewPlain(plainConfig(), fetch.WithLogger(slog.New(slog.DiscardHandler))).Fetch(context.Background(), fetch.Request{URL: srv.URL})
	assertRefused(t, err, "127.0.0.1")
	if hits.Load() != 0 {
		t.Error("the local server was reached")
	}
	if _, err := localPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: srv.URL}); err != nil {
		t.Errorf("with the loopback allowance: %v", err)
	}
}

func TestNewRefusalDoesNotFallBackToFirecrawl(t *testing.T) {
	var calls atomic.Int32
	firecrawl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"rendered","metadata":{"statusCode":200}}}`))
	}))
	defer firecrawl.Close()
	cfg := plainConfig()
	cfg.FirecrawlAPIKey = "fc-key"
	cfg.FirecrawlBaseURL = firecrawl.URL
	cfg.FirecrawlTimeout = cfg.Timeout

	_, err := fetch.New(cfg, fetch.WithLogger(slog.New(slog.DiscardHandler))).Fetch(t.Context(), fetch.Request{URL: "http://169.254.169.254/latest/meta-data/"})
	assertRefused(t, err, "169.254.169.254")
	if calls.Load() != 0 {
		t.Errorf("firecrawl called %d times", calls.Load())
	}
}

func TestFirecrawlRefusesNonPublicTargets(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"rendered","metadata":{"statusCode":200}}}`))
	}))
	defer srv.Close()

	for _, target := range []string{
		"http://127.0.0.1/",
		"http://localhost:5432/",
		"http://LOCALHOST./",
		"http://db.localhost/",
		"http://[::1]/",
		"http://169.254.169.254/latest/meta-data/",
		"https://10.0.0.1/",
		"http://100.64.0.1/",
		"http://[fd00::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://0.0.0.0/",
		"http://2130706433/",
		"http://0x7f.1/",
		"http://127.1/",
	} {
		t.Run(target, func(t *testing.T) {
			logger, logs := capturedLogger()
			_, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL), fetch.WithLogger(logger)).Fetch(t.Context(), fetch.Request{URL: target})
			if !errors.Is(err, fetch.ErrForbiddenAddress) || fetch.IsRetryable(err) {
				t.Fatalf("err = %v, want a permanent ErrForbiddenAddress", err)
			}
			if n := strings.Count(logs.String(), "fetch refused a non-public address"); n != 1 {
				t.Errorf("logged %d refusals, want 1:\n%s", n, logs)
			}
		})
	}
	if calls.Load() != 0 {
		t.Errorf("firecrawl called %d times", calls.Load())
	}

	if _, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL)).Fetch(t.Context(), fetch.Request{URL: "https://93.184.216.34/agenda"}); err != nil {
		t.Errorf("public literal: %v", err)
	}
	if _, err := fetch.NewFirecrawl(firecrawlConfig(srv.URL), loopbackOnly()...).Fetch(t.Context(), fetch.Request{URL: "http://localhost/agenda"}); err != nil {
		t.Errorf("localhost with the loopback allowance: %v", err)
	}
}
