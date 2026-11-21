package rctc_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/rctc"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

var recordedAt = time.Date(2026, 11, 19, 20, 30, 0, 0, time.UTC)

const (
	pageURL    = "https://www.rctc.org/meetings-and-agendas/"
	listingURL = "https://2kbyogxrg4.execute-api.us-west-2.amazonaws.com/5fe25821fa91b30007ea7498/playlist/Riverside%20County%20Transportation%20Commission"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := fs.ReadFile(os.DirFS("testdata"), name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

var noVideos = []byte(`{"videos":[]}`)

func serving(body []byte) *fetchtest.Fake {
	return servingWith(body, noVideos)
}

func servingWith(body, listing []byte) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if req.URL == listingURL {
			return fetch.Response{Status: 200, ContentType: "application/json", Body: listing, FinalURL: req.URL}, nil
		}
		return fetch.Response{Status: 200, ContentType: "text/html; charset=UTF-8", Body: body, FinalURL: req.URL}, nil
	}}
}

func logged(t *testing.T, cfg rctc.Config, fetcher fetch.Fetcher, now time.Time) (*rctc.Adapter, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a, err := rctc.New(cfg, sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(now), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	return a, &buf
}

func checkWindow(now time.Time) sources.Window {
	return sources.Window{From: now.AddDate(0, 0, -30), To: now.AddDate(0, 0, 90)}
}

func meetings(t *testing.T, body []byte, now time.Time, window sources.Window) ([]sources.Meeting, string) {
	t.Helper()
	a, buf := logged(t, rctc.RCTC, serving(body), now)
	got, err := a.Meetings(t.Context(), window)
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	return got, buf.String()
}

func warnings(logs string) []string {
	var out []string
	for line := range strings.Lines(logs) {
		if strings.Contains(line, "level=WARN") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

func describe(ms []sources.Meeting) string {
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "%s %q %s %s agenda=%s\n", m.SourceID, m.Title, m.StartsAt.Format(time.RFC3339), m.Status, m.AgendaURL)
		if m.RecordingURL != "" {
			fmt.Fprintf(&b, "  recording=%s\n", m.RecordingURL)
		}
	}
	return b.String()
}

func TestRecordedPageGivesTheCommissionMeetingOfTheDailyWindow(t *testing.T) {
	fake := servingWith(fixture(t, "rctc_meetings_and_agendas.html"), fixture(t, "castus_rctc_playlist.json"))
	a, buf := logged(t, rctc.RCTC, fake, recordedAt)
	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	want := sources.Meeting{
		SourceID:     "commission-meeting/2026-11",
		Title:        "Commission Meeting",
		StartsAt:     time.Date(2026, 11, 10, 17, 30, 0, 0, time.UTC),
		Status:       sources.StatusHeld,
		PageURL:      pageURL,
		AgendaURL:    "https://www.rctc.org/november2026agenda",
		RecordingURL: "https://dlttx48mxf9m3.cloudfront.net/outputs/6af62b4f05fc227eedc662d8/Default/HLS/out.m3u8",
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("meetings =\n%s\nwant %+v", describe(got), want)
	}
	calls := fake.Calls()
	if len(calls) != 2 || calls[0].URL != pageURL || calls[0].MaxBytes != rctc.MaxPageBytes ||
		calls[1].URL != listingURL || calls[1].MaxBytes != rctc.MaxListingBytes {
		t.Errorf("requests = %+v, want the page capped at %d bytes, then the castus listing capped at %d", calls, rctc.MaxPageBytes, rctc.MaxListingBytes)
	}
	if w := warnings(buf.String()); len(w) != 0 {
		t.Errorf("warnings on the recorded page:\n%s", strings.Join(w, "\n"))
	}
}

func TestRecordedPageListsEveryCommissionMeetingAndNoCommittee(t *testing.T) {
	year := sources.Window{From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	got, logs := meetings(t, fixture(t, "rctc_meetings_and_agendas.html"), recordedAt, year)
	want := "" +
		"commission-meeting/2026-05 \"Commission Meeting\" 2026-05-13T16:30:00Z scheduled agenda=https://www.rctc.org/may2026agenda\n" +
		"commission-meeting/2026-06 \"Commission Meeting\" 2026-06-10T16:30:00Z scheduled agenda=https://www.rctc.org/june2026agenda\n" +
		"commission-meeting/2026-08 \"Commission Meeting\" 2026-08-12T16:30:00Z scheduled agenda=https://www.rctc.org/august2026agenda\n" +
		"commission-meeting/2026-09 \"Commission Meeting\" 2026-09-09T16:30:00Z scheduled agenda=https://www.rctc.org/september2026agenda\n" +
		"commission-meeting/2026-10 \"Commission Meeting\" 2026-10-14T16:30:00Z scheduled agenda=https://www.rctc.org/october2026agenda\n" +
		"commission-meeting/2026-11 \"Commission Meeting\" 2026-11-10T17:30:00Z scheduled agenda=https://www.rctc.org/november2026agenda\n"
	if d := describe(got); d != want {
		t.Errorf("meetings =\n%s\nwant\n%s", d, want)
	}
	if w := warnings(logs); len(w) != 0 {
		t.Errorf("warnings on the recorded page:\n%s", strings.Join(w, "\n"))
	}
	if n := strings.Count(logs, "is not a board meeting"); n != 23 {
		t.Errorf("committee rows dropped = %d, want 23", n)
	}
}

func TestRecordedPageDropsCancelledCommitteeRowsSilently(t *testing.T) {
	body := fixture(t, "rctc_meetings_and_agendas.html")
	if !bytes.Contains(body, []byte("EXECUTIVE COMMITTEE-CANCELED")) {
		t.Fatal("fixture lost its cancelled committee rows")
	}
	_, logs := meetings(t, body, recordedAt, checkWindow(recordedAt))
	if strings.Contains(logs, "level=INFO") || len(warnings(logs)) != 0 {
		t.Errorf("committee rows logged above debug:\n%s", logs)
	}
}

func TestDailyRunsKeepTheSameSourceIDs(t *testing.T) {
	body := fixture(t, "rctc_meetings_and_agendas.html")
	var first []sources.Meeting
	for i := range 40 {
		now := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		got, _ := meetings(t, body, now, checkWindow(now))
		ids := map[time.Time]string{}
		for _, m := range got {
			ids[m.StartsAt] = m.SourceID
		}
		for _, m := range first {
			if id, ok := ids[m.StartsAt]; ok && id != m.SourceID {
				t.Errorf("day %d: meeting at %s has id %s, was %s", i, m.StartsAt, id, m.SourceID)
			}
		}
		first = append(first, got...)
	}
}
