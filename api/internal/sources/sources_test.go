package sources_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

func TestDepsLogDefaultsToADiscardingLogger(t *testing.T) {
	if (sources.Deps{}).Log() == nil {
		t.Fatal("Log() = nil")
	}
	logger := slog.New(slog.DiscardHandler)
	if got := (sources.Deps{Logger: logger}).Log(); got != logger {
		t.Errorf("Log() = %p, want the configured logger %p", got, logger)
	}
}

func validMeeting() sources.Meeting {
	return sources.Meeting{
		SourceID:     "evt-1",
		Title:        "Regular Board Meeting",
		StartsAt:     time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC),
		Status:       sources.StatusScheduled,
		PageURL:      "https://agency.test/meetings/1",
		AgendaURL:    "https://agency.test/meetings/1/agenda.pdf",
		RecordingURL: "",
	}
}

func TestValidateAcceptsWellFormedMeetings(t *testing.T) {
	second := validMeeting()
	second.SourceID = "evt-2"
	second.Status = sources.StatusHeld
	second.AgendaURL = ""
	second.RecordingURL = "https://agency.test/meetings/2.mp4"
	if err := sources.Validate([]sources.Meeting{validMeeting(), second}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := sources.Validate(nil); err != nil {
		t.Fatalf("Validate(nil): %v", err)
	}
}

func TestValidateRejectsContractViolations(t *testing.T) {
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*sources.Meeting){
		"empty source id":     func(m *sources.Meeting) { m.SourceID = "" },
		"blank title":         func(m *sources.Meeting) { m.Title = "  " },
		"zero start":          func(m *sources.Meeting) { m.StartsAt = time.Time{} },
		"start not in UTC":    func(m *sources.Meeting) { m.StartsAt = m.StartsAt.In(pacific) },
		"unknown status":      func(m *sources.Meeting) { m.Status = "postponed" },
		"empty page url":      func(m *sources.Meeting) { m.PageURL = "" },
		"relative page url":   func(m *sources.Meeting) { m.PageURL = "/meetings/1" },
		"ftp agenda url":      func(m *sources.Meeting) { m.AgendaURL = "ftp://agency.test/a.pdf" },
		"bad recording url":   func(m *sources.Meeting) { m.RecordingURL = "https://agency.test/%zz" },
		"hostless agenda url": func(m *sources.Meeting) { m.AgendaURL = "https:///a.pdf" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := validMeeting()
			mutate(&m)
			err := sources.Validate([]sources.Meeting{m})
			if !errors.Is(err, sources.ErrInvalidMeeting) {
				t.Fatalf("Validate = %v, want ErrInvalidMeeting", err)
			}
		})
	}
}

func TestValidateRejectsDuplicateSourceIDs(t *testing.T) {
	err := sources.Validate([]sources.Meeting{validMeeting(), validMeeting()})
	if !errors.Is(err, sources.ErrInvalidMeeting) {
		t.Fatalf("Validate = %v, want ErrInvalidMeeting", err)
	}
}

func TestRetryableFollowsFetchConventions(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"retryable fetch error", &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}, true},
		{"wrapped retryable fetch error", fmt.Errorf("list events: %w", &fetch.Error{Retryable: true, Err: errors.New("timeout")}), true},
		{"deadline", fmt.Errorf("list: %w", context.DeadlineExceeded), true},
		{"canceled", context.Canceled, true},
		{"permanent fetch error", &fetch.Error{StatusCode: 404, Err: errors.New("not found")}, false},
		{"too large", fetch.ErrTooLarge, false},
		{"parse error", errors.New("unexpected token"), false},
		{"invalid meeting", sources.ErrInvalidMeeting, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sources.Retryable(tc.err); got != tc.want {
				t.Errorf("Retryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRegistryBuildsEachAdapterWithItsDependencies(t *testing.T) {
	fetcher := fetchtest.Returning("ok")
	clock := sourcestest.NewClock(time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC))
	adapter := sourcestest.Returning()
	var got sources.Deps
	registry := sources.NewRegistry(sources.Deps{Fetcher: fetcher, Clock: clock}, map[string]sources.Constructor{
		"la-metro": func(deps sources.Deps) sources.Adapter {
			got = deps
			return adapter
		},
	})

	found, ok := registry.Lookup("la-metro")
	if !ok || found != adapter {
		t.Fatalf("Lookup(la-metro) = %v, %v", found, ok)
	}
	if got.Fetcher != fetcher || got.Clock != clock {
		t.Errorf("constructor deps = %+v", got)
	}
	if _, ok := registry.Lookup("octa"); ok {
		t.Error("Lookup(octa) found an adapter that was never registered")
	}
	if slugs := registry.Slugs(); len(slugs) != 1 || slugs[0] != "la-metro" {
		t.Errorf("Slugs() = %v", slugs)
	}
}

func TestNilRegistryHasNoAdapters(t *testing.T) {
	var registry *sources.Registry
	if _, ok := registry.Lookup("la-metro"); ok {
		t.Error("nil registry found an adapter")
	}
	if slugs := registry.Slugs(); len(slugs) != 0 {
		t.Errorf("Slugs() = %v", slugs)
	}
}

func TestSystemClockIsUTC(t *testing.T) {
	now := sources.SystemClock{}.Now()
	if now.Location() != time.UTC || time.Since(now) > time.Minute {
		t.Errorf("SystemClock.Now() = %v", now)
	}
}

func TestWindowContains(t *testing.T) {
	w := sources.Window{
		From: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2027, 2, 15, 0, 0, 0, 0, time.UTC),
	}
	if !w.Contains(w.From) || !w.Contains(w.To) || !w.Contains(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("Contains rejected a time inside the window")
	}
	if w.Contains(w.From.Add(-time.Second)) || w.Contains(w.To.Add(time.Second)) {
		t.Error("Contains accepted a time outside the window")
	}
}
