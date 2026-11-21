package fetch_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

const testAgent = "CivicScannerTest/1.0 (+https://example.test/bot)"

func plainConfig() config.Fetch {
	return config.Fetch{
		UserAgent:    testAgent,
		Timeout:      5 * time.Second,
		MaxBytes:     1 << 20,
		MaxRedirects: 3,
	}
}

func loopbackOnly() []fetch.Option {
	return []fetch.Option{fetch.AllowAddress(netip.Addr.IsLoopback), fetch.WithLogger(slog.New(slog.DiscardHandler))}
}

func localPlain(cfg config.Fetch) fetch.Fetcher {
	return fetch.NewPlain(cfg, loopbackOnly()...)
}

type agentLog struct {
	mu     sync.Mutex
	agents []string
}

func (l *agentLog) record(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.agents = append(l.agents, r.Header.Get("User-Agent"))
}

func (l *agentLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.agents...)
}

func TestPlainReturnsPage(t *testing.T) {
	log := &agentLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<h1>Agenda</h1>"))
	}))
	defer srv.Close()

	got, err := localPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: srv.URL + "/meetings"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Status != 200 || got.ContentType != "text/html; charset=utf-8" || string(got.Body) != "<h1>Agenda</h1>" {
		t.Errorf("response = %+v", got)
	}
	if got.FinalURL != srv.URL+"/meetings" {
		t.Errorf("FinalURL = %q", got.FinalURL)
	}
	if a := log.all(); len(a) != 1 || a[0] != testAgent {
		t.Errorf("User-Agent = %q, want %q", a, testAgent)
	}
}

func TestPlainSendsUserAgentOnEveryRedirect(t *testing.T) {
	log := &agentLog{}
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		_, _ = w.Write([]byte("done"))
	}))
	defer final.Close()
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		http.Redirect(w, r, final.URL+"/final", http.StatusFound)
	}))
	defer hop.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		http.Redirect(w, r, hop.URL+"/hop", http.StatusMovedPermanently)
	}))
	defer first.Close()

	got, err := localPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: first.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.FinalURL != final.URL+"/final" {
		t.Errorf("FinalURL = %q", got.FinalURL)
	}
	agents := log.all()
	if len(agents) != 3 {
		t.Fatalf("saw %d requests, want 3", len(agents))
	}
	for i, a := range agents {
		if a != testAgent {
			t.Errorf("request %d User-Agent = %q, want %q", i, a, testAgent)
		}
	}
}

func TestPlainStopsAfterTooManyRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	_, err := localPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: srv.URL})
	if !errors.Is(err, fetch.ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects", err)
	}
	if fetch.IsRetryable(err) {
		t.Error("redirect loop must be permanent")
	}
}

func TestPlainRejectsBodyOverCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	cfg := plainConfig()
	cfg.MaxBytes = 150
	_, err := localPlain(cfg).Fetch(context.Background(), fetch.Request{URL: srv.URL})
	if !errors.Is(err, fetch.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if fetch.IsRetryable(err) {
		t.Error("too large must be permanent")
	}
}

func TestPlainRejectsDeclaredLengthOverCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer srv.Close()

	_, err := localPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: srv.URL, MaxBytes: 100})
	if !errors.Is(err, fetch.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestPlainPerCallCapRaisesLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer srv.Close()

	cfg := plainConfig()
	cfg.MaxBytes = 100
	got, err := localPlain(cfg).Fetch(context.Background(), fetch.Request{URL: srv.URL, MaxBytes: 1000})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Body) != 500 {
		t.Errorf("body length = %d", len(got.Body))
	}
}

func TestPlainStatusErrors(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{http.StatusNotFound, false},
		{http.StatusGone, false},
		{http.StatusForbidden, false},
		{http.StatusUnauthorized, false},
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tt.status)
		}))
		_, err := localPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: srv.URL})
		srv.Close()

		var fe *fetch.Error
		if !errors.As(err, &fe) {
			t.Fatalf("status %d: err = %v, want *fetch.Error", tt.status, err)
		}
		if fe.StatusCode != tt.status {
			t.Errorf("status %d: StatusCode = %d", tt.status, fe.StatusCode)
		}
		if fetch.IsRetryable(err) != tt.retryable {
			t.Errorf("status %d: IsRetryable = %v, want %v", tt.status, fetch.IsRetryable(err), tt.retryable)
		}
	}
}

func TestPlainReadsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := localPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: srv.URL})
	var fe *fetch.Error
	if !errors.As(err, &fe) || fe.RetryAfter != 7*time.Second {
		t.Fatalf("err = %v, want RetryAfter 7s", err)
	}
}

func TestPlainTimeoutIsRetryable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	cfg := plainConfig()
	cfg.Timeout = 50 * time.Millisecond
	_, err := localPlain(cfg).Fetch(context.Background(), fetch.Request{URL: srv.URL})
	if err == nil || !fetch.IsRetryable(err) {
		t.Fatalf("err = %v, want retryable", err)
	}
}

func TestPlainNetworkFailureIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := localPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: url})
	if err == nil || !fetch.IsRetryable(err) {
		t.Fatalf("err = %v, want retryable", err)
	}
}

func TestPlainCanceledContextIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := localPlain(plainConfig()).Fetch(ctx, fetch.Request{URL: srv.URL})
	if err == nil || !fetch.IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want retryable wrapping context.Canceled", err)
	}
}

func TestPlainRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "not a url", "ftp://example.test/file", "file:///etc/passwd", "http://"} {
		_, err := fetch.NewPlain(plainConfig()).Fetch(context.Background(), fetch.Request{URL: u})
		if err == nil || fetch.IsRetryable(err) {
			t.Errorf("url %q: err = %v, want permanent error", u, err)
		}
	}
}
