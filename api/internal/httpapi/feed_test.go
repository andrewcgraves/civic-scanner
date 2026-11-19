package httpapi

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden feed")

const (
	testWebURL = "https://civic.example"
	golden     = "testdata/agency_feed.xml"
)

func flagOn(context.Context, string) bool  { return true }
func flagOff(context.Context, string) bool { return false }

func ptr[T any](v T) *T { return &v }

func goldenFeed() store.AgencyFeed {
	meeting := uuid.MustParse("6f1c2a43-1d1e-4b8e-9a51-0c3b8e7d2f10")
	return store.AgencyFeed{
		Agency: store.Agency{ID: uuid.MustParse("0b6f5f0e-4a0d-4f0c-8f43-5c6d7e8f9a0b"), Slug: "la-metro", Name: "LA Metro", Website: "https://www.metro.net"},
		Items: []store.FeedItem{
			{
				EventID:         uuid.MustParse("c3d4e5f6-0000-4000-8000-000000000003"),
				Type:            store.EventTranscriptReady,
				OccurredAt:      time.Date(2027, 1, 30, 4, 15, 0, 0, time.UTC),
				MeetingID:       meeting,
				MeetingTitle:    "Regular Board Meeting",
				MeetingStartsAt: time.Date(2027, 1, 29, 2, 0, 0, 0, time.UTC),
			},
			{
				EventID:         uuid.MustParse("b2c3d4e5-0000-4000-8000-000000000002"),
				Type:            store.EventAgendaChanged,
				OccurredAt:      time.Date(2027, 1, 27, 16, 0, 0, 0, time.UTC),
				MeetingID:       meeting,
				MeetingTitle:    "Regular Board Meeting",
				MeetingStartsAt: time.Date(2027, 1, 29, 2, 0, 0, 0, time.UTC),
				AgendaSummary:   ptr("The board will vote on the K Line extension.\nPublic comment opens at 6 PM."),
				AgendaChange:    ptr("Item 7, the bus lane contract, was added."),
			},
			{
				EventID:         uuid.MustParse("a1b2c3d4-0000-4000-8000-000000000001"),
				Type:            store.EventAgendaPublished,
				OccurredAt:      time.Date(2027, 1, 22, 16, 0, 0, 0, time.UTC),
				MeetingID:       meeting,
				MeetingTitle:    "Regular Board Meeting",
				MeetingStartsAt: time.Date(2027, 1, 29, 2, 0, 0, 0, time.UTC),
				AgendaSummary:   ptr("The board will vote on the K Line extension & fares."),
			},
		},
	}
}

func feedRouter(s Store, enabled func(context.Context, string) bool, webURL string) http.Handler {
	logger := slog.New(slog.DiscardHandler)
	return NewRouter(NewHandler(s, logger, "test", WithFlags(enabled), WithPublicWebURL(webURL)), logger)
}

func serveFeed(t *testing.T, h http.Handler, method, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type parsedFeed struct {
	Version string `xml:"version,attr"`
	Channel struct {
		Title string `xml:"title"`
		Links []struct {
			XMLName xml.Name
			Href    string `xml:"href,attr"`
			Rel     string `xml:"rel,attr"`
			Value   string `xml:",chardata"`
		} `xml:"link"`
		Link          string                     `xml:"-"`
		Self          struct{ Href, Rel string } `xml:"-"`
		Description   string                     `xml:"description"`
		LastBuildDate string                     `xml:"lastBuildDate"`
		Items         []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			GUID        struct {
				IsPermaLink string `xml:"isPermaLink,attr"`
				Value       string `xml:",chardata"`
			} `xml:"guid"`
			PubDate string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

func parseFeed(t *testing.T, body []byte) parsedFeed {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = true
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("feed is not well-formed XML: %v\n%s", err, body)
		}
	}
	var f parsedFeed
	if err := xml.Unmarshal(body, &f); err != nil {
		t.Fatalf("unmarshal feed: %v", err)
	}
	for _, l := range f.Channel.Links {
		if l.XMLName.Space == atomNamespace {
			f.Channel.Self.Href, f.Channel.Self.Rel = l.Href, l.Rel
		} else {
			f.Channel.Link = l.Value
		}
	}
	return f
}

func TestAgencyFeedMatchesGolden(t *testing.T) {
	h := feedRouter(fakeStore{feeds: map[string]store.AgencyFeed{"la-metro": goldenFeed()}}, flagOn, testWebURL)
	rec := serveFeed(t, h, http.MethodGet, "/v1/agencies/la-metro/feed.xml", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/rss+xml; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if etag := rec.Header().Get("ETag"); len(etag) != 34 || etag[0] != '"' || etag[33] != '"' {
		t.Errorf("ETag = %q, want a quoted strong validator", etag)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(rec.Body.Len()) {
		t.Errorf("Content-Length = %q, body is %d bytes", cl, rec.Body.Len())
	}

	if *updateGolden {
		if err := os.WriteFile(golden, rec.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("feed differs from %s (run go test -run TestAgencyFeedMatchesGolden -update):\n%s", golden, rec.Body)
	}

	f := parseFeed(t, rec.Body.Bytes())
	if f.Version != "2.0" || f.Channel.Title != "LA Metro meetings" || f.Channel.Link != testWebURL+"/agencies/la-metro" {
		t.Errorf("channel = %+v", f.Channel)
	}
	if f.Channel.Self.Href != testWebURL+"/agencies/la-metro/feed.xml" || f.Channel.Self.Rel != "self" {
		t.Errorf("atom self link = %+v", f.Channel.Self)
	}
	if f.Channel.LastBuildDate != "Sat, 30 Jan 2027 04:15:00 +0000" {
		t.Errorf("lastBuildDate = %q", f.Channel.LastBuildDate)
	}
	if len(f.Channel.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(f.Channel.Items))
	}
	wantTitles := []string{
		"Transcript ready: Regular Board Meeting (Jan 28, 2027)",
		"Agenda updated: Regular Board Meeting (Jan 28, 2027)",
		"Agenda posted: Regular Board Meeting (Jan 28, 2027)",
	}
	for i, item := range f.Channel.Items {
		src := goldenFeed().Items[i]
		if item.Title != wantTitles[i] {
			t.Errorf("item %d title = %q, want %q", i, item.Title, wantTitles[i])
		}
		if item.GUID.Value != src.EventID.String() || item.GUID.IsPermaLink != "false" {
			t.Errorf("item %d guid = %+v", i, item.GUID)
		}
		if item.Link != testWebURL+"/meetings/"+src.MeetingID.String() {
			t.Errorf("item %d link = %q", i, item.Link)
		}
		if _, err := time.Parse(time.RFC1123Z, item.PubDate); err != nil || !strings.HasSuffix(item.PubDate, "+0000") {
			t.Errorf("item %d pubDate = %q", i, item.PubDate)
		}
	}
	if d := f.Channel.Items[1].Description; !strings.Contains(d, "<p>What changed: Item 7, the bus lane contract, was added.</p>") ||
		!strings.Contains(d, "<p>Summary: The board will vote on the K Line extension.</p><p>Public comment opens at 6 PM.</p>") ||
		!strings.Contains(d, "Thursday, January 28, 2027 at 6:00 PM PST") {
		t.Errorf("agenda changed description = %q", d)
	}
	if d := f.Channel.Items[2].Description; !strings.Contains(d, "extension &amp; fares.") {
		t.Errorf("agenda posted description = %q, want the summary HTML-escaped", d)
	}
}

func TestAgencyFeedNeutralizesHostileText(t *testing.T) {
	hostile := "Board ]]> <script>alert(1)</script> & <b>bold</b> \x00\x01\x08\x0b\x1b[31m\x7f\u0085 \u202eevil\u202c \u2066iso\u2069 zero\u200bwidth \ufeffbom \u00adsoft \U000E0041tag \xff\xfe bad \xed\xa0\x80 surrogate \ufffe\uffff end"
	long := strings.Repeat("word ", 20000)
	meeting := uuid.New()
	feed := store.AgencyFeed{
		Agency: store.Agency{ID: uuid.New(), Slug: "hostile agency/../x", Name: hostile},
		Items: []store.FeedItem{
			{
				EventID: uuid.New(), Type: store.EventAgendaChanged, OccurredAt: time.Now(), MeetingID: meeting,
				MeetingTitle: hostile + long, MeetingStartsAt: time.Now(),
				AgendaSummary: ptr(hostile + "\n" + long), AgendaChange: ptr(hostile),
			},
			{
				EventID: uuid.New(), Type: store.EventAgendaPublished, OccurredAt: time.Now(), MeetingID: meeting,
				MeetingTitle: "\x00\u202e\t \n", MeetingStartsAt: time.Now(),
			},
		},
	}
	h := feedRouter(fakeStore{feeds: map[string]store.AgencyFeed{"hostile": feed}}, flagOn, testWebURL)
	rec := serveFeed(t, h, http.MethodGet, "/v1/agencies/hostile/feed.xml", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	raw := rec.Body.String()
	for _, banned := range []string{"]]>", "<script", "CDATA", "<b>", "\x00", "\u202e", "\ufeff"} {
		if strings.Contains(raw, banned) {
			t.Errorf("raw feed contains %q", banned)
		}
	}
	if !utf8.ValidString(raw) {
		t.Error("feed is not valid UTF-8")
	}

	f := parseFeed(t, rec.Body.Bytes())
	if !strings.HasPrefix(f.Channel.Link, testWebURL+"/agencies/hostile%20agency%2F..%2Fx") {
		t.Errorf("channel link = %q, want the slug path-escaped", f.Channel.Link)
	}
	texts := []string{f.Channel.Title}
	for _, item := range f.Channel.Items {
		texts = append(texts, item.Title)
		if len(item.Title) > 2*maxFeedTitleBytes {
			t.Errorf("title is %d bytes", len(item.Title))
		}
		if len(item.Description) > 4*maxFeedTextBytes {
			t.Errorf("description is %d bytes", len(item.Description))
		}
		markup := strings.NewReplacer("<p>", "", "</p>", "").Replace(item.Description)
		if strings.ContainsAny(markup, "<>") {
			t.Errorf("description has markup other than paragraphs: %q", item.Description)
		}
		texts = append(texts, markup)
	}
	for _, text := range texts {
		for _, r := range text {
			if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Cf, r) || r == 0xFFFE || r == 0xFFFF || r == utf8.RuneError || (r >= 0xE0000 && r <= 0xE007F) {
				t.Errorf("text keeps %U: %q", r, text)
				break
			}
		}
	}
	if d := f.Channel.Items[0].Description; !strings.Contains(d, "<p>What changed: Board ]]&gt; &lt;script&gt;alert(1)&lt;/script&gt; &amp; &lt;b&gt;bold&lt;/b&gt; [31m") {
		t.Errorf("description does not keep the text escaped: %q", d)
	}
	if got := f.Channel.Items[1].Title; !strings.HasPrefix(got, "Agenda posted: Meeting (") {
		t.Errorf("blank meeting title = %q, want a fallback", got)
	}
	if got := f.Channel.Items[0].Title; !strings.HasPrefix(got, "Agenda updated: Board ]]> <script>alert(1)</script> & <b>bold</b> [31m") || !strings.Contains(got, "…") {
		t.Errorf("hostile title = %q, want the text kept as plain text and capped", got)
	}
}

func TestAgencyFeedConditionalRequests(t *testing.T) {
	h := feedRouter(fakeStore{feeds: map[string]store.AgencyFeed{"la-metro": goldenFeed()}}, flagOn, testWebURL)
	const path = "/v1/agencies/la-metro/feed.xml"
	first := serveFeed(t, h, http.MethodGet, path, nil)
	etag := first.Header().Get("ETag")

	for _, tc := range []struct {
		name, ifNoneMatch string
		want              int
	}{
		{"same etag", etag, http.StatusNotModified},
		{"weak form", "W/" + etag, http.StatusNotModified},
		{"in a list", `"stale", ` + etag, http.StatusNotModified},
		{"any", "*", http.StatusNotModified},
		{"other etag", `"stale"`, http.StatusOK},
		{"empty", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveFeed(t, h, http.MethodGet, path, http.Header{"If-None-Match": {tc.ifNoneMatch}})
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if rec.Header().Get("ETag") != etag || rec.Header().Get("Cache-Control") != feedCacheControl {
				t.Errorf("headers = %v", rec.Header())
			}
			if tc.want == http.StatusNotModified && rec.Body.Len() != 0 {
				t.Errorf("304 has a body: %q", rec.Body)
			}
		})
	}

	if rec := serveFeed(t, h, http.MethodGet, path, http.Header{"If-None-Match": {`"stale"`, etag}}); rec.Code != http.StatusNotModified {
		t.Errorf("several If-None-Match lines = %d %s, want 304", rec.Code, rec.Body)
	}
	if rec := serveFeed(t, h, http.MethodGet, path, http.Header{"If-None-Match": {`"stale"`, `"older"`}}); rec.Code != http.StatusOK {
		t.Errorf("several non-matching If-None-Match lines = %d, want 200", rec.Code)
	}

	head := serveFeed(t, h, http.MethodHead, path, nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != etag ||
		head.Header().Get("Content-Length") != strconv.Itoa(first.Body.Len()) || head.Header().Get("Content-Type") != "application/rss+xml; charset=utf-8" {
		t.Errorf("HEAD = %d %v %q", head.Code, head.Header(), head.Body)
	}
	if rec := serveFeed(t, h, http.MethodHead, path, http.Header{"If-None-Match": {etag}}); rec.Code != http.StatusNotModified {
		t.Errorf("conditional HEAD = %d, want 304", rec.Code)
	}

	changed := goldenFeed()
	changed.Items[2].AgendaSummary = ptr("A different summary.")
	h = feedRouter(fakeStore{feeds: map[string]store.AgencyFeed{"la-metro": changed}}, flagOn, testWebURL)
	rec := serveFeed(t, h, http.MethodGet, path, http.Header{"If-None-Match": {etag}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == etag {
		t.Errorf("after a summary changed: %d with ETag %q, want 200 and a new ETag", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestABackdatedChangeKeepsItsPlaceAndItsFoundTime(t *testing.T) {
	feed := goldenFeed()
	found := feed.Items[len(feed.Items)-1].OccurredAt.Add(-48 * time.Hour)
	feed.Items[0].OccurredAt = found
	h := feedRouter(fakeStore{feeds: map[string]store.AgencyFeed{"la-metro": feed}}, flagOn, testWebURL)
	f := parseFeed(t, serveFeed(t, h, http.MethodGet, "/v1/agencies/la-metro/feed.xml", nil).Body.Bytes())
	if len(f.Channel.Items) != 3 || f.Channel.Items[0].GUID.Value != feed.Items[0].EventID.String() {
		t.Fatalf("items = %+v, want the sequence order kept", f.Channel.Items)
	}
	if f.Channel.Items[0].PubDate != found.UTC().Format(time.RFC1123Z) {
		t.Errorf("pubDate = %q, want the found time", f.Channel.Items[0].PubDate)
	}
	if f.Channel.LastBuildDate != feed.Items[1].OccurredAt.UTC().Format(time.RFC1123Z) {
		t.Errorf("lastBuildDate = %q, want the newest pubDate", f.Channel.LastBuildDate)
	}
}

func TestAgencyFeedEmpty(t *testing.T) {
	feed := store.AgencyFeed{Agency: store.Agency{ID: uuid.New(), Slug: "quiet", Name: "Quiet Agency"}}
	h := feedRouter(fakeStore{feeds: map[string]store.AgencyFeed{"quiet": feed}}, flagOn, testWebURL)
	rec := serveFeed(t, h, http.MethodGet, "/v1/agencies/quiet/feed.xml", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	f := parseFeed(t, rec.Body.Bytes())
	if len(f.Channel.Items) != 0 || f.Channel.LastBuildDate != "" || f.Channel.Title != "Quiet Agency meetings" {
		t.Errorf("empty feed = %+v", f.Channel)
	}
}

func TestAgencyFeedUnavailableIsTheSame404(t *testing.T) {
	calls := 0
	s := fakeStore{feeds: map[string]store.AgencyFeed{"la-metro": goldenFeed()}, feedCalls: &calls}
	unknown := serveFeed(t, feedRouter(s, flagOn, testWebURL), http.MethodGet, "/v1/agencies/nope/feed.xml", nil)
	if unknown.Code != http.StatusNotFound || !strings.Contains(unknown.Body.String(), `"code":"not_found"`) {
		t.Fatalf("unknown = %d %s", unknown.Code, unknown.Body)
	}
	calls = 0
	for name, h := range map[string]http.Handler{
		"flag off":       feedRouter(s, flagOff, testWebURL),
		"no web URL":     feedRouter(s, flagOn, ""),
		"not configured": NewRouter(NewHandler(s, slog.New(slog.DiscardHandler), "test"), slog.New(slog.DiscardHandler)),
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := serveFeed(t, h, method, "/v1/agencies/la-metro/feed.xml", nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d, want 404", name, method, rec.Code)
			}
			if method == http.MethodGet && rec.Body.String() != unknown.Body.String() {
				t.Errorf("%s: body = %s, want the unknown agency's %s", name, rec.Body, unknown.Body)
			}
		}
	}
	if calls != 0 {
		t.Errorf("store called %d times while feeds are unavailable", calls)
	}
}

func TestAgencyFeedStoreError(t *testing.T) {
	h := feedRouter(fakeStore{feedErr: errors.New("boom")}, flagOn, testWebURL)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := serveFeed(t, h, method, "/v1/agencies/la-metro/feed.xml", nil)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", method, rec.Code)
		}
		if method == http.MethodGet && !strings.Contains(rec.Body.String(), `"code":"internal_error"`) {
			t.Errorf("body = %s", rec.Body)
		}
	}
}

func TestAgendaPostedWithoutSummaryStillReads(t *testing.T) {
	item, _ := feedRenderer{webURL: testWebURL}.item(store.FeedItem{
		EventID: uuid.New(), Type: store.EventAgendaPublished, MeetingID: uuid.New(),
		MeetingTitle: "Finance Committee", MeetingStartsAt: time.Date(2027, 7, 1, 17, 30, 0, 0, time.UTC),
	}, "OCTA")
	if item.Title != "Agenda posted: Finance Committee (Jul 1, 2027)" {
		t.Errorf("title = %q", item.Title)
	}
	want := "<p>OCTA posted the agenda for Finance Committee, Thursday, July 1, 2027 at 10:30 AM PDT.</p><p>A summary is not available yet.</p>"
	if item.Description != want {
		t.Errorf("description = %q, want %q", item.Description, want)
	}
	changed, _ := feedRenderer{webURL: testWebURL}.item(store.FeedItem{Type: store.EventAgendaChanged, MeetingTitle: "Finance Committee"}, "OCTA")
	if strings.Contains(changed.Description, "What changed") {
		t.Errorf("description without a change note = %q", changed.Description)
	}
}

func TestTruncateKeepsRuneBoundaries(t *testing.T) {
	s := strings.Repeat("é", 10)
	got := truncate(s, 8)
	if !utf8.ValidString(got) || len(got) > 8 || !strings.HasSuffix(got, ellipsis) {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("short", 8); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
}
