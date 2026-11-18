package featureflags

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-feature/go-sdk/openfeature"
)

const testCacheTTL = 30 * time.Second

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type countingPostHog struct {
	srv      *httptest.Server
	requests atomic.Int32
	failing  atomic.Bool
	mu       sync.Mutex
	flags    map[string]bool
	ids      map[string]int
}

func newCountingPostHog(t *testing.T, flags map[string]bool) *countingPostHog {
	t.Helper()
	fake := &countingPostHog{flags: flags, ids: map[string]int{}}
	fake.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.requests.Add(1)
		var req struct {
			DistinctID string `json:"distinct_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		fake.mu.Lock()
		fake.ids[req.DistinctID]++
		details := make(map[string]flagDetail, len(fake.flags))
		for key, enabled := range fake.flags {
			details[key] = flagDetail{Key: key, Enabled: enabled}
		}
		fake.mu.Unlock()
		if fake.failing.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(flagsResponse{Flags: details})
	}))
	t.Cleanup(fake.srv.Close)
	return fake
}

func (f *countingPostHog) set(flag string, enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flags[flag] = enabled
}

func (f *countingPostHog) requestsFor(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ids[id]
}

func newCachedProvider(t *testing.T, host string, ttl time.Duration, clock *fakeClock) *posthogProvider {
	t.Helper()
	cfg := postHogConfig(host)
	cfg.PostHogCacheTTL = ttl
	p, err := newPostHogProvider(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("newPostHogProvider: %v", err)
	}
	if clock != nil {
		p.cache.now = clock.Now
	}
	t.Cleanup(p.Shutdown)
	return p
}

func enabledFor(ctx context.Context, p *posthogProvider, flag, targetingKey string) bool {
	flat := openfeature.FlattenedContext{}
	if targetingKey != "" {
		flat[openfeature.TargetingKey] = targetingKey
	}
	return p.BooleanEvaluation(ctx, flag, false, flat).Value
}

func holdingPostHog(t *testing.T, flag string) (*httptest.Server, *atomic.Int32, chan struct{}) {
	t.Helper()
	var requests atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(flagsResponse{Flags: map[string]flagDetail{flag: {Key: flag, Enabled: true}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &requests, release
}

func TestCacheServesRepeatedChecksWithoutANetworkCall(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": true})
	p := newCachedProvider(t, fake.srv.URL, testCacheTTL, newFakeClock())

	for range 50 {
		if !enabledFor(t.Context(), p, "new-search", "") {
			t.Fatal("new-search evaluated to off")
		}
	}
	if n := fake.requests.Load(); n != 1 {
		t.Errorf("PostHog received %d requests for 50 checks, want 1", n)
	}
}

func TestCacheServesEveryFlagFromOneResponse(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": true, "digest-emails": false, "beta-map": true})
	p := newCachedProvider(t, fake.srv.URL, testCacheTTL, newFakeClock())

	want := map[string]bool{"new-search": true, "digest-emails": false, "beta-map": true, "never-defined": false}
	for flag, on := range want {
		if got := enabledFor(t.Context(), p, flag, ""); got != on {
			t.Errorf("%s = %v, want %v", flag, got, on)
		}
	}
	if n := fake.requests.Load(); n != 1 {
		t.Errorf("PostHog received %d requests for 4 flags, want 1", n)
	}
}

func TestCacheKeepsIdentitiesApart(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": true})
	p := newCachedProvider(t, fake.srv.URL, testCacheTTL, newFakeClock())

	for _, key := range []string{"", "user-1", "user-2", "user-1", ""} {
		enabledFor(t.Context(), p, "new-search", key)
	}
	if n := fake.requests.Load(); n != 3 {
		t.Errorf("PostHog received %d requests for 3 identities, want 3", n)
	}
	for _, id := range []string{serverDistinctID, "user-1", "user-2"} {
		if n := fake.requestsFor(id); n != 1 {
			t.Errorf("identity %q requested %d times, want 1", id, n)
		}
	}
}

func TestCacheRefreshesAfterTTL(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": false})
	clock := newFakeClock()
	p := newCachedProvider(t, fake.srv.URL, testCacheTTL, clock)

	if enabledFor(t.Context(), p, "new-search", "") {
		t.Fatal("new-search evaluated to on")
	}
	fake.set("new-search", true)
	clock.Advance(testCacheTTL - time.Second)
	if enabledFor(t.Context(), p, "new-search", "") {
		t.Error("the cache refreshed before the TTL elapsed")
	}
	clock.Advance(time.Second)
	if !enabledFor(t.Context(), p, "new-search", "") {
		t.Error("the cache did not refresh once the TTL elapsed")
	}
	if n := fake.requests.Load(); n != 2 {
		t.Errorf("PostHog received %d requests, want 2", n)
	}
}

func TestZeroTTLDisablesTheCache(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": true})
	p := newCachedProvider(t, fake.srv.URL, 0, nil)

	for range 3 {
		if !enabledFor(t.Context(), p, "new-search", "") {
			t.Fatal("new-search evaluated to off")
		}
	}
	if n := fake.requests.Load(); n != 3 {
		t.Errorf("PostHog received %d requests with the cache disabled, want 3", n)
	}
}

func TestConcurrentChecksShareOneRequest(t *testing.T) {
	srv, requests, release := holdingPostHog(t, "new-search")
	p := newCachedProvider(t, srv.URL, testCacheTTL, newFakeClock())

	const callers = 25
	results := make(chan bool, callers)
	for range callers {
		go func() { results <- enabledFor(t.Context(), p, "new-search", "") }()
	}
	for requests.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range callers {
		if !<-results {
			t.Error("a concurrent check evaluated to off")
		}
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("PostHog received %d requests for %d concurrent checks, want 1", n, callers)
	}
}

func TestCallerDeadlineDoesNotCancelTheSharedRefresh(t *testing.T) {
	srv, requests, release := holdingPostHog(t, "new-search")
	p := newCachedProvider(t, srv.URL, testCacheTTL, newFakeClock())

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if enabledFor(ctx, p, "new-search", "") {
		t.Error("a flag evaluated to on while PostHog hung")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("the check returned after %v, want it to stop at the 50ms context deadline", elapsed)
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for !enabledFor(t.Context(), p, "new-search", "") {
		if time.Now().After(deadline) {
			t.Fatal("the abandoned refresh never populated the cache")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("PostHog received %d requests, want 1", n)
	}
}

func TestFlagNeverFetchedIsOffAndFailureIsNotRetriedWithinTTL(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": true})
	fake.failing.Store(true)
	clock := newFakeClock()
	p := newCachedProvider(t, fake.srv.URL, testCacheTTL, clock)

	for range 5 {
		if enabledFor(t.Context(), p, "new-search", "") {
			t.Fatal("a flag never fetched evaluated to on")
		}
	}
	if n := fake.requests.Load(); n != 1 {
		t.Errorf("PostHog received %d requests during an outage within one TTL, want 1", n)
	}

	fake.failing.Store(false)
	clock.Advance(testCacheTTL)
	if !enabledFor(t.Context(), p, "new-search", "") {
		t.Error("the cache did not retry and recover once the TTL elapsed")
	}
}

func TestLastGoodValueIsServedForTheGracePeriodThenOff(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": true})
	clock := newFakeClock()
	p := newCachedProvider(t, fake.srv.URL, testCacheTTL, clock)

	if !enabledFor(t.Context(), p, "new-search", "") {
		t.Fatal("new-search evaluated to off")
	}
	fake.failing.Store(true)

	clock.Advance(testCacheTTL)
	if !enabledFor(t.Context(), p, "new-search", "") {
		t.Error("the last good value was not served just after the TTL while PostHog failed")
	}

	clock.Advance(staleGraceMultiple*testCacheTTL - testCacheTTL)
	if !enabledFor(t.Context(), p, "new-search", "") {
		t.Error("the last good value was not served at the end of the grace period")
	}

	clock.Advance(time.Second)
	if enabledFor(t.Context(), p, "new-search", "") {
		t.Error("a stale value was served past the grace period")
	}
}

func TestRecoveryAfterFailureServesFreshValue(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": true})
	clock := newFakeClock()
	p := newCachedProvider(t, fake.srv.URL, testCacheTTL, clock)

	enabledFor(t.Context(), p, "new-search", "")
	fake.failing.Store(true)
	clock.Advance(testCacheTTL)
	enabledFor(t.Context(), p, "new-search", "")

	fake.failing.Store(false)
	fake.set("new-search", false)
	clock.Advance(testCacheTTL)
	if enabledFor(t.Context(), p, "new-search", "") {
		t.Error("a recovered PostHog answer was ignored in favour of the stale value")
	}
}

func TestCacheIsBoundedInSize(t *testing.T) {
	fake := newCountingPostHog(t, map[string]bool{"new-search": true})
	clock := newFakeClock()
	p := newCachedProvider(t, fake.srv.URL, testCacheTTL, clock)
	p.cache.maxEntries = 2

	for _, key := range []string{"user-1", "user-2", "user-3"} {
		enabledFor(t.Context(), p, "new-search", key)
		clock.Advance(time.Second)
	}
	if n := p.cache.size(); n != 2 {
		t.Errorf("the cache holds %d identities, want at most 2", n)
	}
	enabledFor(t.Context(), p, "new-search", "user-3")
	enabledFor(t.Context(), p, "new-search", "user-2")
	if n := fake.requests.Load(); n != 3 {
		t.Errorf("PostHog received %d requests, want 3 while the two newest identities stay cached", n)
	}
	enabledFor(t.Context(), p, "new-search", "user-1")
	if n := fake.requests.Load(); n != 4 {
		t.Errorf("PostHog received %d requests, want 4 once the oldest identity was evicted", n)
	}
}

func TestCachedMultivariateFlagIsOff(t *testing.T) {
	control := "control"
	srv := fakePostHogResponse(t, flagsResponse{Flags: map[string]flagDetail{
		"experiment-a": {Key: "experiment-a", Enabled: true, Variant: &control},
	}})
	p := newCachedProvider(t, srv.URL, testCacheTTL, newFakeClock())

	if enabledFor(t.Context(), p, "experiment-a", "") {
		t.Error("a multivariate flag evaluated to on")
	}
}
