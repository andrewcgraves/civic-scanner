package granicus_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/granicus"
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

func TestUnreadableFeedsArePermanent(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"markdown from firecrawl", []byte("# Metrolink\n\nBoard of Directors")},
		{"html page", []byte("<html><body>Service Unavailable</body></html>")},
		{"rss without a channel", []byte(`<rss version="2.0"></rss>`)},
	}
	for _, tc := range tests {
		for _, feed := range []string{"agendas", "vod"} {
			t.Run(tc.name+" in "+feed, func(t *testing.T) {
				agendas, vod := tc.body, rss()
				if feed == "vod" {
					agendas, vod = rss(), tc.body
				}
				_, err := metrolink(t, serving(agendas, vod)).Meetings(t.Context(), checkWindow(recordedAt))
				if !errors.Is(err, granicus.ErrMalformed) || sources.Retryable(err) {
					t.Errorf("err = %v, want permanent ErrMalformed", err)
				}
			})
		}
	}
}

func unreadable() map[string]entry {
	good := clipItem(1438, "2026-11-13 10:00")
	relative := good
	relative.link = "/AgendaViewer.php?view_id=8&clip_id=1438"
	noID := good
	noID.link = "https://metrolink.granicus.com/AgendaViewer.php?view_id=8"
	badID := good
	badID.link = "https://metrolink.granicus.com/AgendaViewer.php?view_id=8&clip_id=x&event_id=0"
	eventWithoutDate := eventItem(612, "2026-12-11", "2026-12-04 09:00").titled("Board of Directors")
	clipWithoutAnyDate := good.titled("Board of Directors")
	clipWithoutAnyDate.parts = ""
	return map[string]entry{
		"relative link":                   relative,
		"link without an id":              noID,
		"link with bad ids":               badID,
		"event without a title date":      eventWithoutDate,
		"clip without a date of any kind": clipWithoutAnyDate,
	}
}

func TestAllUnreadableBoardItemsInTheWindowArePermanent(t *testing.T) {
	for name, bad := range unreadable() {
		t.Run(name, func(t *testing.T) {
			_, err := metrolink(t, serving(rss(bad), rss())).Meetings(t.Context(), checkWindow(recordedAt))
			if !errors.Is(err, granicus.ErrMalformed) || sources.Retryable(err) {
				t.Errorf("err = %v, want permanent ErrMalformed", err)
			}
		})
	}
}

func TestUnreadableBoardItemInTheWindowIsSkippedWhenOthersAreReadable(t *testing.T) {
	for name, bad := range unreadable() {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			a, err := granicus.New(granicus.Metrolink, sources.Deps{
				Fetcher: serving(rss(bad, clipItem(1436, "2026-10-23 10:15")), rss(videoItem(1436, "2026-10-23 10:15"))),
				Clock:   sourcestest.NewClock(recordedAt),
				Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].SourceID != "board-of-directors/2026-10-23" {
				t.Errorf("meetings = %+v, want only the readable one", got)
			}
			if !strings.Contains(logs.String(), "skipped an unreadable board item") {
				t.Errorf("logs = %s, want the skipped item logged", logs.String())
			}
		})
	}
}

func TestUnreadableBoardItemOutsideTheWindowIsSkipped(t *testing.T) {
	old := clipItem(1300, "2025-01-24 10:00")
	old.link = "/AgendaViewer.php?view_id=8&clip_id=1300"
	got, err := metrolink(t, serving(rss(old), rss())).Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil || len(got) != 0 {
		t.Errorf("meetings = %+v, err = %v, want none and no error", got, err)
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
		{"blank board name", func(c *granicus.Config) { c.BoardNames = []string{"Board of Directors", " - "} }},
		{"no time zone", func(c *granicus.Config) { c.TimeZone = "" }},
		{"unknown time zone", func(c *granicus.Config) { c.TimeZone = "America/Nowhere" }},
		{"no start time", func(c *granicus.Config) { c.StartTime = "" }},
		{"bad start time", func(c *granicus.Config) { c.StartTime = "10am" }},
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
