package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-feature/go-sdk/openfeature"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

func TestFeedFlagIsEvaluatedAnonymouslyWhateverTheToken(t *testing.T) {
	var keys []string
	var mu sync.Mutex
	staffOnly := func(ctx context.Context, flag string) bool {
		key := openfeature.TransactionContext(ctx).TargetingKey()
		mu.Lock()
		keys = append(keys, flag+"="+key)
		mu.Unlock()
		return key == "user_staff"
	}
	s := fakeStore{feeds: map[string]store.AgencyFeed{"la-metro": goldenFeed()}}
	authn := &fakeAuth{tokens: map[string]auth.Identity{"staff-token": {UserID: "user_staff"}}}
	logger := slog.New(slog.DiscardHandler)
	h := NewRouter(NewHandler(s, logger, "test", WithAuthenticator(authn), WithFlags(staffOnly), WithPublicWebURL(testWebURL)), logger)

	signedIn := serveFeed(t, h, http.MethodGet, "/v1/agencies/la-metro/feed.xml", http.Header{"Authorization": {"Bearer staff-token"}})
	anonymous := serveFeed(t, h, http.MethodGet, "/v1/agencies/la-metro/feed.xml", nil)
	if signedIn.Code != http.StatusNotFound || anonymous.Code != http.StatusNotFound || signedIn.Body.String() != anonymous.Body.String() {
		t.Errorf("signed in = %d %s, anonymous = %d %s; want the same anonymous 404", signedIn.Code, signedIn.Body, anonymous.Code, anonymous.Body)
	}
	if len(authn.seen) != 1 || authn.seen[0] != "staff-token" {
		t.Fatalf("token verified %v, want the staff token once", authn.seen)
	}
	for _, k := range keys {
		if k != FlagAgencyFeeds+"=" {
			t.Errorf("flag evaluated as %q, want %s with no targeting key", k, FlagAgencyFeeds)
		}
	}
	if signedIn.Header().Get("Vary") != "" {
		t.Errorf("Vary = %q, want none", signedIn.Header().Get("Vary"))
	}
}

func TestConditionalRequestsAreServedFromTheCache(t *testing.T) {
	calls := 0
	h := feedRouter(fakeStore{feeds: map[string]store.AgencyFeed{"la-metro": goldenFeed()}, feedCalls: &calls}, flagOn, testWebURL)
	const path = "/v1/agencies/la-metro/feed.xml"
	etag := serveFeed(t, h, http.MethodGet, path, nil).Header().Get("ETag")
	for range 5 {
		if rec := serveFeed(t, h, http.MethodGet, path, http.Header{"If-None-Match": {etag}}); rec.Code != http.StatusNotModified {
			t.Fatalf("status = %d, want 304", rec.Code)
		}
	}
	serveFeed(t, h, http.MethodHead, path, nil)
	if calls != 1 {
		t.Errorf("store called %d times, want 1", calls)
	}
}

func TestTheFlagIsCheckedBeforeTheCache(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	calls := 0
	enabled := func(context.Context, string) bool { return on.Load() }
	h := feedRouter(fakeStore{feeds: map[string]store.AgencyFeed{"la-metro": goldenFeed()}, feedCalls: &calls}, enabled, testWebURL)
	const path = "/v1/agencies/la-metro/feed.xml"
	if rec := serveFeed(t, h, http.MethodGet, path, nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	on.Store(false)
	if rec := serveFeed(t, h, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
		t.Errorf("cached feed with the flag off = %d, want 404", rec.Code)
	}
}

func testCache(now *time.Time) *feedCache {
	c := newFeedCache()
	c.now = func() time.Time { return *now }
	return c
}

func TestFeedCacheExpiresAfterTheTTL(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	c := testCache(&now)
	loads := 0
	load := func(context.Context, string) (renderedFeed, error) {
		loads++
		return renderedFeed{etag: fmt.Sprint(loads)}, nil
	}
	for range 3 {
		if f, err := c.get(t.Context(), "a", load); err != nil || f.etag != "1" {
			t.Fatalf("get = %+v, %v", f, err)
		}
	}
	if feedCacheTTL > 300*time.Second {
		t.Errorf("TTL %s is longer than max-age", feedCacheTTL)
	}
	now = now.Add(feedCacheTTL)
	if f, _ := c.get(t.Context(), "a", load); f.etag != "2" || loads != 2 {
		t.Errorf("after the TTL: %+v after %d loads, want a reload", f, loads)
	}
}

func TestFeedCacheDoesNotKeepErrors(t *testing.T) {
	now := time.Now()
	c := testCache(&now)
	loads := 0
	load := func(context.Context, string) (renderedFeed, error) {
		loads++
		return renderedFeed{}, store.ErrNotFound
	}
	for range 2 {
		if _, err := c.get(t.Context(), "nope", load); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	}
	if loads != 2 || len(c.entries) != 0 {
		t.Errorf("loads = %d, entries = %d; want errors retried and not stored", loads, len(c.entries))
	}
}

func TestFeedCacheRunsOneLoadForABurst(t *testing.T) {
	now := time.Now()
	c := testCache(&now)
	release := make(chan struct{})
	var loads atomic.Int32
	load := func(ctx context.Context, _ string) (renderedFeed, error) {
		loads.Add(1)
		<-release
		return renderedFeed{etag: "x"}, ctx.Err()
	}

	const callers = 20
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			f, err := c.get(t.Context(), "busy", load)
			if err == nil && f.etag != "x" {
				err = fmt.Errorf("etag %q", f.etag)
			}
			errs <- err
		})
	}
	for {
		c.mu.Lock()
		running := c.inflight["busy"] != nil
		c.mu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := loads.Load(); n != 1 {
		t.Errorf("loads = %d, want 1", n)
	}
}

func TestFeedCacheWaiterStopsWithItsContextAndTheLoadFinishes(t *testing.T) {
	now := time.Now()
	c := testCache(&now)
	release := make(chan struct{})
	load := func(ctx context.Context, _ string) (renderedFeed, error) {
		<-release
		return renderedFeed{etag: "done"}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.get(ctx, "slow", load); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	close(release)
	if f, err := c.get(t.Context(), "slow", load); err != nil || f.etag != "done" {
		t.Errorf("after the load = %+v, %v", f, err)
	}
}

func TestFeedCacheIsBounded(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	c := testCache(&now)
	c.maxSize = 3
	load := func(_ context.Context, slug string) (renderedFeed, error) { return renderedFeed{etag: slug}, nil }
	for i := range 3 {
		now = now.Add(time.Second)
		if _, err := c.get(t.Context(), fmt.Sprint(i), load); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Second)
	if _, err := c.get(t.Context(), "new", load); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.entries["0"]; ok || len(c.entries) != 3 {
		t.Errorf("entries = %v, want the oldest evicted", c.entries)
	}
	now = now.Add(feedCacheTTL)
	if _, err := c.get(t.Context(), "later", load); err != nil {
		t.Fatal(err)
	}
	if len(c.entries) != 1 {
		t.Errorf("entries = %v, want the expired ones dropped", c.entries)
	}
}

func TestFeedAllowsGetAndHead(t *testing.T) {
	h := feedRouter(fakeStore{}, flagOn, testWebURL)
	rec := serveFeed(t, h, http.MethodPost, "/v1/agencies/la-metro/feed.xml", nil)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST feed = %d Allow %q, want 405 GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}
	rec = serveFeed(t, h, http.MethodPost, "/v1/agencies/la-metro", nil)
	if rec.Header().Get("Allow") != "GET" {
		t.Errorf("POST agency Allow = %q, want GET", rec.Header().Get("Allow"))
	}
}

type failingWriter struct{ after int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, errors.New("disk full")
	}
	w.after--
	return len(p), nil
}

func TestRendererReportsWriteErrors(t *testing.T) {
	for after := range 3 {
		err := feedRenderer{webURL: testWebURL}.write(&failingWriter{after: after}, goldenFeed())
		if err == nil {
			t.Errorf("write failing after %d writes: no error", after)
		}
	}
}

func TestRendererSkipsUnknownEventTypes(t *testing.T) {
	feed := goldenFeed()
	feed.Items = append([]store.FeedItem{{EventID: uuid.New(), Type: "recording_found", OccurredAt: time.Now(), MeetingID: uuid.New()}}, feed.Items...)
	body, err := feedRenderer{webURL: testWebURL}.render(feed)
	if err != nil {
		t.Fatal(err)
	}
	f := parseFeed(t, body)
	if len(f.Channel.Items) != 3 || f.Channel.Items[0].GUID.Value != goldenFeed().Items[0].EventID.String() {
		t.Errorf("items = %+v, want the unknown event skipped", f.Channel.Items)
	}
	if f.Channel.LastBuildDate != "Sat, 30 Jan 2027 04:15:00 +0000" {
		t.Errorf("lastBuildDate = %q, want the newest published item's", f.Channel.LastBuildDate)
	}
}

func TestRendererCanHoldAgendaItemsUntilTheirSummary(t *testing.T) {
	now := time.Date(2027, 1, 22, 17, 0, 0, 0, time.UTC)
	feed := goldenFeed()
	feed.Items[2].AgendaSummary = nil
	r := feedRenderer{webURL: testWebURL, now: func() time.Time { return now }, summaryGrace: 2 * time.Hour}
	body, err := r.render(feed)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(parseFeed(t, body).Channel.Items); got != 2 {
		t.Errorf("within the grace period: %d items, want the unsummarized agenda held back", got)
	}
	now = now.Add(2 * time.Hour)
	body, _ = r.render(feed)
	if got := len(parseFeed(t, body).Channel.Items); got != 3 {
		t.Errorf("after the grace period: %d items, want 3", got)
	}
	feed.Items[1].AgendaSummary = nil
	feed.Items[1].OccurredAt = now
	body, _ = r.render(feed)
	if got := len(parseFeed(t, body).Channel.Items); got != 2 {
		t.Errorf("fresh unsummarized change: %d items, want it held back", got)
	}
}

func TestRenderedFeedSizeIsCapped(t *testing.T) {
	worst := strings.Repeat("&", 50000)
	feed := store.AgencyFeed{Agency: store.Agency{Slug: "big", Name: worst}}
	for i := range store.MaxFeedItems {
		feed.Items = append(feed.Items, store.FeedItem{
			EventID: uuid.New(), Type: store.EventAgendaChanged, OccurredAt: time.Unix(int64(1_800_000_000-i), 0),
			MeetingID: uuid.New(), MeetingTitle: worst, AgendaSummary: ptr(worst), AgendaChange: ptr(worst),
		})
	}
	body, err := feedRenderer{webURL: testWebURL}.render(feed)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > maxFeedBytes {
		t.Errorf("feed is %d bytes, cap is %d", len(body), maxFeedBytes)
	}
	f := parseFeed(t, body)
	if len(f.Channel.Items) == 0 || len(f.Channel.Items) == store.MaxFeedItems || f.Channel.Items[0].GUID.Value != feed.Items[0].EventID.String() {
		t.Errorf("kept %d items, want the newest ones and fewer than %d", len(f.Channel.Items), store.MaxFeedItems)
	}

	full, err := feedRenderer{webURL: testWebURL}.render(goldenFeed())
	if err != nil {
		t.Fatal(err)
	}
	limit := len(full) - 1
	small, err := feedRenderer{webURL: testWebURL, maxBytes: limit}.render(goldenFeed())
	if err != nil {
		t.Fatal(err)
	}
	if len(small) > limit || len(parseFeed(t, small).Channel.Items) != 2 {
		t.Errorf("%d-byte cap: %d bytes, %d items; want 2 items within the cap", limit, len(small), len(parseFeed(t, small).Channel.Items))
	}
}

func TestPlainTextHandlesCarriageReturnsAndBlankLines(t *testing.T) {
	if got := plainText("one\r\ntwo\r\rthree\n\n \t \nfour", 100); got != "one\ntwo\nthree\nfour" {
		t.Errorf("plainText = %q", got)
	}
	if got := plainLine("a\r\nb\rc", 100); got != "a b c" {
		t.Errorf("plainLine = %q", got)
	}
	if got := htmlParagraphs("a\n\nb", ""); got != "<p>a</p><p>b</p>" {
		t.Errorf("htmlParagraphs = %q", got)
	}
}
