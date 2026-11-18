package legistar_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/legistar"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

func TestFetchErrorsKeepTheirRetryability(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"server error", &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("status 503")}, true},
		{"rate limited", &fetch.Error{StatusCode: 429, Retryable: true, Err: errors.New("status 429")}, true},
		{"bad request", &fetch.Error{StatusCode: 400, Err: errors.New("status 400")}, false},
		{"too large", &fetch.Error{StatusCode: 200, Err: fetch.ErrTooLarge}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := laMetro(t, fetchtest.Failing(tc.err)).Meetings(t.Context(), checkWindow(recordedAt))
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.err)
			}
			if got := sources.Retryable(err); got != tc.retryable {
				t.Errorf("Retryable = %v, want %v", got, tc.retryable)
			}
		})
	}
}

func TestCanceledContextIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := laMetro(t, serving([]byte("[]"))).Meetings(ctx, checkWindow(recordedAt))
	if err == nil || !sources.Retryable(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
}

func TestMalformedResponsesArePermanent(t *testing.T) {
	good := boardEvent(1, "2026-10-22", "10:00 AM")
	tests := []struct {
		name string
		body []byte
	}{
		{"not json", []byte("<html>Service Unavailable</html>")},
		{"an object, not a list", []byte(`{"Message":"An error has occurred."}`)},
		{"wrong field type", []byte(`[{"EventId":"one"}]`)},
		{"event without an id", encode(t, good.with("EventId", 0))},
		{"unparseable date", encode(t, good.with("EventDate", "22/10/2026"))},
		{"no body name", encode(t, good.with("EventBodyName", " "))},
		{"no meeting page", encode(t, good.with("EventInSiteURL", nil))},
		{"relative meeting page", encode(t, good.with("EventInSiteURL", "/MeetingDetail.aspx?LEGID=1"))},
		{"relative agenda", encode(t, good.with("EventAgendaFile", "meetings/2026/10/1_A.pdf"))},
		{"relative video path", encode(t, good.with("EventVideoPath", "video/1.mp4"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := laMetro(t, serving(tc.body)).Meetings(t.Context(), checkWindow(recordedAt))
			if !errors.Is(err, legistar.ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			if sources.Retryable(err) {
				t.Errorf("malformed response is retryable: %v", err)
			}
		})
	}
}

func TestAPageThatRepeatsEarlierEventsStopsPaging(t *testing.T) {
	page := encode(t, boardEvent(1, "2026-10-01", "10:00 AM"), boardEvent(2, "2026-10-08", "10:00 AM"))
	fake := serving(page)
	cfg := laMetroConfig()
	cfg.PageSize = 2
	a, err := legistar.New(cfg, sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(recordedAt)})
	if err != nil {
		t.Fatal(err)
	}

	_, err = a.Meetings(t.Context(), checkWindow(recordedAt))
	if !errors.Is(err, legistar.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	if n := len(fake.Calls()); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		change func(*legistar.Config)
	}{
		{"no client", func(c *legistar.Config) { c.Client = "" }},
		{"client with a slash", func(c *legistar.Config) { c.Client = "metro/events" }},
		{"no body ids", func(c *legistar.Config) { c.BodyIDs = nil }},
		{"zero body id", func(c *legistar.Config) { c.BodyIDs = []int{138, 0} }},
		{"no time zone", func(c *legistar.Config) { c.TimeZone = "" }},
		{"unknown time zone", func(c *legistar.Config) { c.TimeZone = "America/Nowhere" }},
		{"negative page size", func(c *legistar.Config) { c.PageSize = -1 }},
		{"relative base url", func(c *legistar.Config) { c.BaseURL = "webapi.legistar.com" }},
		{"granicus host without a view", func(c *legistar.Config) { c.Granicus = legistar.Granicus{Host: "metro.granicus.com"} }},
		{"granicus host with a path", func(c *legistar.Config) { c.Granicus.Host = "metro.granicus.com/x" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := laMetroConfig()
			tc.change(&cfg)
			if _, err := legistar.New(cfg, sources.Deps{}); !errors.Is(err, legistar.ErrInvalidConfig) {
				t.Errorf("New err = %v, want ErrInvalidConfig", err)
			}
			defer func() {
				if recover() == nil {
					t.Error("Constructor did not panic")
				}
			}()
			legistar.Constructor(cfg)
		})
	}
}

func TestConstructorBuildsAnAdapterFromDeps(t *testing.T) {
	fake := serving(fixture(t, "metro_board_events.json"))
	adapter := legistar.Constructor(legistar.LAMetro)(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(recordedAt)})

	got, err := adapter.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || len(fake.Calls()) != 1 {
		t.Errorf("meetings = %d, requests = %d, want 6 and 1", len(got), len(fake.Calls()))
	}
}

func TestBaseURLOverride(t *testing.T) {
	fake := serving([]byte("[]"))
	cfg := laMetroConfig()
	cfg.BaseURL = "http://127.0.0.1:8080/"
	meetingsWith(t, cfg, fake)
	calls := fake.Calls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].URL, "http://127.0.0.1:8080/v1/metro/events?") {
		t.Errorf("requests = %+v", calls)
	}
}
