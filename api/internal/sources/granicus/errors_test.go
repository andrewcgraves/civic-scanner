package granicus_test

import (
	"context"
	"errors"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/granicus"
)

func TestFetchErrorsKeepTheirRetryability(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"server error", &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("status 503")}, true},
		{"rate limited", &fetch.Error{StatusCode: 429, Retryable: true, Err: errors.New("status 429")}, true},
		{"not found", &fetch.Error{StatusCode: 404, Err: errors.New("status 404")}, false},
		{"too large", &fetch.Error{StatusCode: 200, Err: fetch.ErrTooLarge}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := metrolink(t, fetchtest.Failing(tc.err)).Meetings(t.Context(), checkWindow(recordedAt))
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.err)
			}
			if got := sources.Retryable(err); got != tc.retryable {
				t.Errorf("Retryable = %v, want %v", got, tc.retryable)
			}
		})
	}
}

func TestVideoFeedErrorIsReturned(t *testing.T) {
	want := &fetch.Error{StatusCode: 502, Retryable: true, Err: errors.New("status 502")}
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if req.URL == vodFeed {
			return fetch.Response{}, want
		}
		return fetch.Response{Status: 200, Body: rss(), FinalURL: req.URL}, nil
	}}
	_, err := metrolink(t, fake).Meetings(t.Context(), checkWindow(recordedAt))
	if !errors.Is(err, want) || !sources.Retryable(err) {
		t.Errorf("err = %v, want retryable %v", err, want)
	}
}

func TestCanceledContextIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := metrolink(t, recorded(t)).Meetings(ctx, checkWindow(recordedAt))
	if err == nil || !sources.Retryable(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
}

func TestMalformedResponsesArePermanent(t *testing.T) {
	good := boardItem(1438, agendaViewer, "2026-11-13 10:00")
	broken := func(change func(*entry)) []byte {
		e := good
		change(&e)
		return rss(e)
	}
	tests := []struct {
		name    string
		agendas []byte
	}{
		{"markdown from firecrawl", []byte("# Metrolink\n\nBoard of Directors")},
		{"html page", []byte("<html><body>Service Unavailable</body></html>")},
		{"rss without a channel", []byte(`<rss version="2.0"></rss>`)},
		{"relative link", broken(func(e *entry) { e.link = "/AgendaViewer.php?view_id=8&clip_id=1438" })},
		{"link without a clip id", broken(func(e *entry) { e.link = "https://metrolink.granicus.com/AgendaViewer.php?view_id=8" })},
		{"link with a bad clip id", broken(func(e *entry) { e.link = "https://metrolink.granicus.com/AgendaViewer.php?view_id=8&clip_id=x" })},
		{"no date parts", broken(func(e *entry) { e.parts = "" })},
		{"non-numeric date part", broken(func(e *entry) { e.parts = "yr='2026' mo='11' day='13' hr='ten' min='00'" })},
		{"month out of range", broken(func(e *entry) { e.parts = "yr='2026' mo='13' day='13' hr='10' min='00'" })},
		{"day out of range", broken(func(e *entry) { e.parts = "yr='2026' mo='02' day='30' hr='10' min='00'" })},
		{"hour out of range", broken(func(e *entry) { e.parts = "yr='2026' mo='11' day='13' hr='24' min='00'" })},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := metrolink(t, serving(tc.agendas, rss())).Meetings(t.Context(), checkWindow(recordedAt))
			if !errors.Is(err, granicus.ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			if sources.Retryable(err) {
				t.Errorf("malformed response is retryable: %v", err)
			}
		})
	}
}

func TestMalformedVideoFeedIsPermanent(t *testing.T) {
	_, err := metrolink(t, serving(rss(), []byte("not xml"))).Meetings(t.Context(), checkWindow(recordedAt))
	if !errors.Is(err, granicus.ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed", err)
	}
}

func TestMalformedCommitteeItemsAreIgnored(t *testing.T) {
	committee := entry{title: "Executive Committee - Nov 13, 2026", link: "/relative"}
	got, err := metrolink(t, serving(rss(committee), rss())).Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil || len(got) != 0 {
		t.Errorf("meetings = %+v, err = %v, want none and no error", got, err)
	}
}

func TestInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		change func(*granicus.Config)
	}{
		{"no host", func(c *granicus.Config) { c.Host = "" }},
		{"host with a path", func(c *granicus.Config) { c.Host = "metrolink.granicus.com/x" }},
		{"host with a port", func(c *granicus.Config) { c.Host = "metrolink.granicus.com:443" }},
		{"zero view id", func(c *granicus.Config) { c.ViewID = 0 }},
		{"no board names", func(c *granicus.Config) { c.BoardNames = nil }},
		{"blank board name", func(c *granicus.Config) { c.BoardNames = []string{"Board of Directors", " "} }},
		{"no time zone", func(c *granicus.Config) { c.TimeZone = "" }},
		{"unknown time zone", func(c *granicus.Config) { c.TimeZone = "America/Nowhere" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := granicus.Metrolink
			cfg.BoardNames = append([]string(nil), cfg.BoardNames...)
			tc.change(&cfg)
			if _, err := granicus.New(cfg, sources.Deps{}); !errors.Is(err, granicus.ErrInvalidConfig) {
				t.Errorf("New err = %v, want ErrInvalidConfig", err)
			}
			defer func() {
				if recover() == nil {
					t.Error("Constructor did not panic")
				}
			}()
			granicus.Constructor(cfg)
		})
	}
}
