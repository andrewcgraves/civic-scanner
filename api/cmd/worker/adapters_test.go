package main

import (
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

func TestLAMetroIsRegisteredAsALegistarAdapter(t *testing.T) {
	fake := fetchtest.Returning("[]")
	now := time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(now)}, adapters)

	adapter, ok := registry.Lookup("la-metro")
	if !ok {
		t.Fatalf("no adapter for la-metro; registered: %v", registry.Slugs())
	}
	if _, err := adapter.Meetings(t.Context(), sources.Window{From: now, To: now.AddDate(0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].URL, "https://webapi.legistar.com/v1/metro/events?") {
		t.Errorf("requests = %+v, want one to the metro Legistar events API", calls)
	}
}

func TestOCTAIsRegisteredAsALegistarAdapterForTheBoardOnly(t *testing.T) {
	fake := fetchtest.Returning("[]")
	now := time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(now)}, adapters)

	adapter, ok := registry.Lookup("octa")
	if !ok {
		t.Fatalf("no adapter for octa; registered: %v", registry.Slugs())
	}
	if _, err := adapter.Meetings(t.Context(), sources.Window{From: now, To: now.AddDate(0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].URL, "https://webapi.legistar.com/v1/octa/events?") || !strings.Contains(calls[0].URL, "%28EventBodyId+eq+138%29") {
		t.Errorf("requests = %+v, want one to the octa Legistar events API for body 138", calls)
	}
}

func TestMetrolinkIsRegisteredAsAGranicusAdapter(t *testing.T) {
	fake := fetchtest.Returning(`<rss version="2.0"><channel></channel></rss>`)
	now := time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(now)}, adapters)

	adapter, ok := registry.Lookup("metrolink")
	if !ok {
		t.Fatalf("no adapter for metrolink; registered: %v", registry.Slugs())
	}
	if _, err := adapter.Meetings(t.Context(), sources.Window{From: now, To: now.AddDate(0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 2 || !strings.HasPrefix(calls[0].URL, "https://metrolink.granicus.com/ViewPublisherRSS.php?view_id=8&") {
		t.Errorf("requests = %+v, want two to the Metrolink Granicus feeds", calls)
	}
}

func TestRCTCIsRegisteredForTheMeetingsPage(t *testing.T) {
	page := `<table><tr><th>DATE &amp; TIME</th><th>MEETING</th></tr><tr><td>NOVEMBER 10, 2026 &#8211; 9:30 AM</td><td><a href="https://www.rctc.org/november2026agenda">COMMISSION MEETING</a></td></tr></table>`
	fake := fetchtest.Returning(page)
	now := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(now)}, adapters)

	adapter, ok := registry.Lookup("rctc")
	if !ok {
		t.Fatalf("no adapter for rctc; registered: %v", registry.Slugs())
	}
	got, err := adapter.Meetings(t.Context(), sources.Window{From: now.AddDate(0, 0, -30), To: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "commission-meeting/2026-11" {
		t.Errorf("meetings = %+v, want the November commission meeting", got)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].URL != "https://www.rctc.org/meetings-and-agendas/" {
		t.Errorf("requests = %+v, want one to the RCTC meetings page", calls)
	}
}

func TestVCTCIsRegisteredForTheCommissionPostsAndItsYouTubeFeed(t *testing.T) {
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if strings.HasPrefix(req.URL, "https://www.youtube.com/") {
			return fetch.Response{Status: 200, ContentType: "text/xml", Body: []byte(`<feed xmlns="http://www.w3.org/2005/Atom"></feed>`), FinalURL: req.URL}, nil
		}
		return fetch.Response{Status: 200, ContentType: "application/json", Body: []byte("[]"), FinalURL: req.URL}, nil
	}}
	now := time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(now)}, adapters)

	adapter, ok := registry.Lookup("vctc")
	if !ok {
		t.Fatalf("no adapter for vctc; registered: %v", registry.Slugs())
	}
	if _, err := adapter.Meetings(t.Context(), sources.Window{From: now, To: now.AddDate(0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, c := range fake.Calls() {
		urls = append(urls, c.URL)
	}
	if len(urls) != 2 || !strings.HasPrefix(urls[0], "https://www.goventura.org/wp-json/wp/v2/meeting?") || !strings.Contains(urls[0], "meeting_type=25%2C33") ||
		urls[1] != "https://www.youtube.com/feeds/videos.xml?channel_id=UCer8W37pepfQH8mhDbTBRjg" {
		t.Errorf("requests = %q, want the goventura.org meeting posts of types 25 and 33, then the VCTC channel feed", urls)
	}
}

func TestSBCTAIsRegisteredAsAPrimeGovAdapter(t *testing.T) {
	fake := fetchtest.Returning("[]")
	now := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(now)}, adapters)

	adapter, ok := registry.Lookup("sbcta")
	if !ok {
		t.Fatalf("no adapter for sbcta; registered: %v", registry.Slugs())
	}
	if _, err := adapter.Meetings(t.Context(), sources.Window{From: now.AddDate(0, 0, -30), To: now.AddDate(0, 0, 90)}); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 2 || calls[0].URL != "https://sbcta.primegov.com/api/v2/PublicPortal/ListUpcomingMeetings" ||
		calls[1].URL != "https://sbcta.primegov.com/api/v2/PublicPortal/ListArchivedMeetings?year=2026" {
		t.Errorf("requests = %+v, want the SBCTA PrimeGov upcoming and 2026 archived lists", calls)
	}
}
