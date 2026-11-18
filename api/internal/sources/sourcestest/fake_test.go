package sourcestest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

var (
	_ sources.Adapter = (*sourcestest.Adapter)(nil)
	_ sources.Clock   = (*sourcestest.Clock)(nil)
)

var window = sources.Window{
	From: time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC),
	To:   time.Date(2027, 2, 15, 13, 0, 0, 0, time.UTC),
}

func TestReturningAnswersWithItsMeetingsAndRecordsWindows(t *testing.T) {
	m := sources.Meeting{SourceID: "a", Title: "Board"}
	fake := sourcestest.Returning(m)

	got, err := fake.Meetings(t.Context(), window)
	if err != nil || len(got) != 1 || got[0] != m {
		t.Fatalf("Meetings = %v, %v", got, err)
	}
	got[0].Title = "mutated"
	again, _ := fake.Meetings(t.Context(), window)
	if again[0].Title != "Board" {
		t.Error("caller mutation leaked into the fake")
	}
	if calls := fake.Calls(); len(calls) != 2 || calls[0] != window {
		t.Errorf("Calls() = %v", calls)
	}
}

func TestSetReplacesMeetingsAndClearsFailure(t *testing.T) {
	fake := sourcestest.Failing(errors.New("down"))
	if _, err := fake.Meetings(t.Context(), window); err == nil {
		t.Fatal("Failing fake returned no error")
	}
	fake.Set(sources.Meeting{SourceID: "b"})
	got, err := fake.Meetings(t.Context(), window)
	if err != nil || len(got) != 1 || got[0].SourceID != "b" {
		t.Fatalf("after Set: %v, %v", got, err)
	}
	want := errors.New("again")
	fake.Fail(want)
	if _, err := fake.Meetings(t.Context(), window); !errors.Is(err, want) {
		t.Fatalf("after Fail: %v", err)
	}
}

func TestMeetingsHonoursCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := sourcestest.Returning().Meetings(ctx, window); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestConstructorReturnsTheGivenAdapter(t *testing.T) {
	fake := sourcestest.Returning()
	registry := sources.NewRegistry(sources.Deps{}, map[string]sources.Constructor{"x": sourcestest.Constructor(fake)})
	if got, ok := registry.Lookup("x"); !ok || got != fake {
		t.Fatalf("Lookup = %v, %v", got, ok)
	}
}

func TestClockSetsAndAdvancesInUTC(t *testing.T) {
	pacific := time.FixedZone("PST", -8*60*60)
	clock := sourcestest.NewClock(time.Date(2026, 11, 17, 5, 0, 0, 0, pacific))
	if got := clock.Now(); got != time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC) {
		t.Errorf("Now() = %v", got)
	}
	clock.Advance(time.Hour)
	if got := clock.Now(); got != time.Date(2026, 11, 17, 14, 0, 0, 0, time.UTC) {
		t.Errorf("after Advance: %v", got)
	}
	clock.Set(time.Date(2026, 12, 1, 0, 0, 0, 0, pacific))
	if got := clock.Now(); got != time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC) {
		t.Errorf("after Set: %v", got)
	}
}
