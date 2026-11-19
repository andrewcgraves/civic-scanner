//go:build integration

package httpapi_test

import (
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func newFeedServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	h := httpapi.NewHandler(store.New(pool), logger, "test",
		httpapi.WithPublicWebURL("https://civic.example"),
		httpapi.WithFlags(func(context.Context, string) bool { return true }))
	srv := httptest.NewServer(httpapi.NewRouter(h, logger))
	t.Cleanup(srv.Close)
	return srv
}

type feedResponse struct {
	StatusCode int
	Header     http.Header
}

func feedRequest(t *testing.T, method, rawURL, ifNoneMatch string) (feedResponse, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return feedResponse{StatusCode: resp.StatusCode, Header: resp.Header}, string(body)
}

type integrationFeed struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			GUID        string `xml:"guid"`
			PubDate     string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

func TestAgencyFeedAgainstPostgres(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "it-feed", "live")
	meetingID := seedMeeting(t, pool, agencyID, "Board <b>&</b> Meeting", time.Date(2027, 3, 4, 2, 0, 0, 0, time.UTC))
	cleanupMeetingChildren(t, pool, meetingID)
	srv := newFeedServer(t, pool)
	feedURL := srv.URL + "/v1/agencies/it-feed/feed.xml"

	resp, body := feedRequest(t, http.MethodGet, feedURL, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "<title>it-feed meetings</title>") || strings.Contains(body, "<item>") {
		t.Fatalf("empty feed = %d %s", resp.StatusCode, body)
	}
	emptyETag := resp.Header.Get("ETag")

	v1, _, err := s.AddAgendaVersion(ctx, store.AgendaVersionInput{MeetingID: meetingID, SourceURL: "https://example.com/a1.pdf", Text: "secret full text one", ContentHash: store.HashContent("one")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaSummary(ctx, v1.ID, "<script>alert(1)</script> summary"); err != nil {
		t.Fatal(err)
	}
	v2, _, err := s.AddAgendaVersion(ctx, store.AgendaVersionInput{MeetingID: meetingID, SourceURL: "https://example.com/a2.pdf", Text: "secret full text two", ContentHash: store.HashContent("two")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaChangeNote(ctx, v2.ID, "Item 3 removed."); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordMeetingEventTx(ctx, tx, store.MeetingEventInput{Type: store.EventTranscriptReady, MeetingID: meetingID, Payload: map[string]string{"storage_key": "audio/secret.mp3"}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListMeetingEvents(ctx, meetingID)
	if err != nil || len(events) != 3 {
		t.Fatalf("events = %+v, %v", events, err)
	}

	if cached, _ := feedRequest(t, http.MethodGet, feedURL, emptyETag); cached.StatusCode != http.StatusNotModified {
		t.Errorf("same server within the TTL = %d, want the cached empty feed's 304", cached.StatusCode)
	}
	srv = newFeedServer(t, pool)
	feedURL = srv.URL + "/v1/agencies/it-feed/feed.xml"
	resp, body = feedRequest(t, http.MethodGet, feedURL, emptyETag)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/rss+xml; charset=utf-8" ||
		resp.Header.Get("Cache-Control") != "public, max-age=300" || resp.Header.Get("ETag") == emptyETag {
		t.Fatalf("feed = %d %v", resp.StatusCode, resp.Header)
	}
	var feed integrationFeed
	if err := xml.Unmarshal([]byte(body), &feed); err != nil {
		t.Fatalf("feed is not XML: %v\n%s", err, body)
	}
	if len(feed.Channel.Items) != 3 {
		t.Fatalf("items = %+v", feed.Channel.Items)
	}
	wantPrefixes := []string{"Transcript ready: ", "Agenda updated: ", "Agenda posted: "}
	for i, item := range feed.Channel.Items {
		e := events[len(events)-1-i]
		if !strings.HasPrefix(item.Title, wantPrefixes[i]+"Board <b>&</b> Meeting (Mar 3, 2027)") || item.GUID != e.ID.String() ||
			item.Link != "https://civic.example/meetings/"+meetingID.String() || item.PubDate != e.OccurredAt.UTC().Format(time.RFC1123Z) {
			t.Errorf("item %d = %+v, want event %+v", i, item, e)
		}
	}
	if d := feed.Channel.Items[1].Description; !strings.Contains(d, "<p>What changed: Item 3 removed.</p>") {
		t.Errorf("changed description = %q", d)
	}
	if d := feed.Channel.Items[2].Description; !strings.Contains(d, "<p>Summary: &lt;script&gt;alert(1)&lt;/script&gt; summary</p>") {
		t.Errorf("published description = %q", d)
	}
	for _, hidden := range []string{"secret", "audio/", "storage_key", "a1.pdf", agencyID.String(), v1.ID.String(), v2.ID.String()} {
		if strings.Contains(body, hidden) {
			t.Errorf("feed exposes %q", hidden)
		}
	}

	etag := resp.Header.Get("ETag")
	if resp, body := feedRequest(t, http.MethodGet, feedURL, etag); resp.StatusCode != http.StatusNotModified || body != "" || resp.Header.Get("ETag") != etag {
		t.Errorf("conditional GET = %d %q", resp.StatusCode, body)
	}
	if head, headBody := feedRequest(t, http.MethodHead, feedURL, ""); head.StatusCode != http.StatusOK || headBody != "" ||
		head.Header.Get("ETag") != etag || head.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("HEAD = %d %v, GET body is %d bytes", head.StatusCode, head.Header, len(body))
	}

	if _, err := s.SetAgendaSummary(ctx, v2.ID, "Now with a summary."); err != nil {
		t.Fatal(err)
	}
	if resp, _ := feedRequest(t, http.MethodGet, feedURL, etag); resp.StatusCode != http.StatusNotModified {
		t.Errorf("within the cache TTL = %d, want the cached 304", resp.StatusCode)
	}
	fresh := newFeedServer(t, pool)
	if resp, body := feedRequest(t, http.MethodGet, fresh.URL+"/v1/agencies/it-feed/feed.xml", etag); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Now with a summary.") {
		t.Errorf("after a summary was written = %d, want 200 with the summary", resp.StatusCode)
	}
}

func TestAgencyFeedNotFoundAgainstPostgres(t *testing.T) {
	pool := testdb.Pool(t)
	seedAgency(t, pool, "it-feed-pending", "pending")
	seedAgency(t, pool, "it-feed-rejected", "rejected")
	srv := newFeedServer(t, pool)

	_, unknown := feedRequest(t, http.MethodGet, srv.URL+"/v1/agencies/it-feed-unknown/feed.xml", "")
	for _, slug := range []string{"it-feed-pending", "it-feed-rejected"} {
		resp, body := feedRequest(t, http.MethodGet, srv.URL+"/v1/agencies/"+slug+"/feed.xml", "")
		if resp.StatusCode != http.StatusNotFound || body != unknown || !strings.Contains(body, `"code":"not_found"`) {
			t.Errorf("%s = %d %q, unknown = %q; want the same 404", slug, resp.StatusCode, body, unknown)
		}
	}

	dark := httptest.NewServer(httpapi.NewRouter(httpapi.NewHandler(store.New(pool), slog.New(slog.DiscardHandler), "test"), slog.New(slog.DiscardHandler)))
	t.Cleanup(dark.Close)
	seedAgency(t, pool, "it-feed-dark", "live")
	if resp, body := feedRequest(t, http.MethodGet, dark.URL+"/v1/agencies/it-feed-dark/feed.xml", ""); resp.StatusCode != http.StatusNotFound || body != unknown {
		t.Errorf("feeds off = %d %q, want the unknown agency's 404", resp.StatusCode, body)
	}
}
