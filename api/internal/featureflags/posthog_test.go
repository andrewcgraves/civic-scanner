package featureflags

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const testPostHogKey = "phc_test"

type flagDetail struct {
	Key     string  `json:"key"`
	Enabled bool    `json:"enabled"`
	Variant *string `json:"variant,omitempty"`
	Failed  *bool   `json:"failed,omitempty"`
}

type flagsResponse struct {
	Flags        map[string]flagDetail `json:"flags"`
	QuotaLimited []string              `json:"quota_limited,omitempty"`
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func postHogConfig(host string) config.FeatureFlags {
	return config.FeatureFlags{PostHogKey: testPostHogKey, PostHogHost: host, PostHogTimeout: time.Second}
}

func fakePostHogResponse(t *testing.T, resp flagsResponse) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/flags/" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			APIKey     string `json:"api_key"`
			DistinctID string `json:"distinct_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.APIKey != testPostHogKey || req.DistinctID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakePostHog(t *testing.T, flags map[string]bool) *httptest.Server {
	t.Helper()
	details := make(map[string]flagDetail, len(flags))
	for key, enabled := range flags {
		details[key] = flagDetail{Key: key, Enabled: enabled}
	}
	return fakePostHogResponse(t, flagsResponse{Flags: details})
}

func hangingPostHog(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv, &requests
}

func TestPostHogDecidesWhenKeySet(t *testing.T) {
	srv := fakePostHog(t, map[string]bool{"new-search": true, "digest-emails": false})
	cfg := postHogConfig(srv.URL)
	cfg.On = []string{"digest-emails"}
	initFlags(t, cfg)

	if !Enabled(t.Context(), "new-search") {
		t.Error("new-search is on in PostHog but evaluated to off")
	}
	if Enabled(t.Context(), "digest-emails") {
		t.Error("digest-emails is off in PostHog but evaluated to on")
	}
}

func TestPostHogUnknownFlagIsOff(t *testing.T) {
	srv := fakePostHog(t, map[string]bool{"new-search": true})
	initFlags(t, postHogConfig(srv.URL))

	if Enabled(t.Context(), "never-defined") {
		t.Error("a flag PostHog does not know evaluated to on")
	}
}

func TestPostHogErrorIsOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	initFlags(t, postHogConfig(srv.URL))

	if Enabled(t.Context(), "new-search") {
		t.Error("a flag evaluated to on while PostHog returned an error")
	}
}

func TestPostHogQuotaLimitedIsOffAndLogged(t *testing.T) {
	srv := fakePostHogResponse(t, flagsResponse{
		Flags:        map[string]flagDetail{"new-search": {Key: "new-search", Enabled: true}},
		QuotaLimited: []string{"feature_flags"},
	})
	var logs lockedBuffer
	shutdown, err := Init(t.Context(), postHogConfig(srv.URL), slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(shutdown)

	if Enabled(t.Context(), "new-search") {
		t.Error("a flag evaluated to on while PostHog was quota limited")
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "quota limited") {
		t.Errorf("expected a warn log about the quota limit, got:\n%s", out)
	}
}

func TestPostHogFailedFlagIsOff(t *testing.T) {
	failed := true
	srv := fakePostHogResponse(t, flagsResponse{
		Flags: map[string]flagDetail{"new-search": {Key: "new-search", Enabled: true, Failed: &failed}},
	})
	initFlags(t, postHogConfig(srv.URL))

	if Enabled(t.Context(), "new-search") {
		t.Error("a flag PostHog failed to evaluate evaluated to on")
	}
}

func TestPostHogMultivariateFlagIsOff(t *testing.T) {
	control, test := "control", "test"
	srv := fakePostHogResponse(t, flagsResponse{Flags: map[string]flagDetail{
		"experiment-a": {Key: "experiment-a", Enabled: true, Variant: &control},
		"experiment-b": {Key: "experiment-b", Enabled: true, Variant: &test},
	}})
	initFlags(t, postHogConfig(srv.URL))

	for _, flag := range []string{"experiment-a", "experiment-b"} {
		if Enabled(t.Context(), flag) {
			t.Errorf("multivariate flag %s evaluated to on", flag)
		}
	}
}

func TestPostHogCheckHonoursContextDeadline(t *testing.T) {
	srv, _ := hangingPostHog(t)
	cfg := postHogConfig(srv.URL)
	cfg.PostHogTimeout = 5 * time.Second
	initFlags(t, cfg)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if Enabled(ctx, "new-search") {
		t.Error("a flag evaluated to on while PostHog hung")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("Enabled returned after %v, want it to stop at the 100ms context deadline", elapsed)
	}
}

func TestPostHogTimeoutIsOffAfterOneAttempt(t *testing.T) {
	srv, requests := hangingPostHog(t)
	cfg := postHogConfig(srv.URL)
	cfg.PostHogTimeout = 100 * time.Millisecond
	initFlags(t, cfg)

	start := time.Now()
	if Enabled(t.Context(), "new-search") {
		t.Error("a flag evaluated to on while PostHog hung")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Enabled returned after %v, want it to stop at the 100ms request timeout", elapsed)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("PostHog received %d requests, want 1 with no retries", n)
	}
}

func TestInitRejectsInvalidPostHogHost(t *testing.T) {
	for _, host := range []string{"://not-a-url", "us.i.posthog.com", "ftp://us.i.posthog.com", "https://"} {
		if _, err := Init(t.Context(), postHogConfig(host), slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("POSTHOG_HOST=%q: expected an error", host)
		}
	}
}

func TestInitAcceptsEmptyPostHogHost(t *testing.T) {
	shutdown, err := Init(t.Context(), postHogConfig(""), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Init with an empty POSTHOG_HOST: %v", err)
	}
	shutdown()
}

func TestPostHogUnreachableIsOff(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	initFlags(t, postHogConfig(srv.URL))

	if Enabled(t.Context(), "new-search") {
		t.Error("a flag evaluated to on while PostHog was unreachable")
	}
}
