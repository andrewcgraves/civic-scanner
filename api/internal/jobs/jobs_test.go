package jobs

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

var discard = slog.New(slog.DiscardHandler)

func flag(on bool) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool { return on && name == FlagCheckAgencies }
}

func checkJob(slug string) *river.Job[CheckAgencyArgs] {
	return &river.Job[CheckAgencyArgs]{
		JobRow: &rivertype.JobRow{ID: 1},
		Args:   CheckAgencyArgs{AgencySlug: slug},
	}
}

func TestCheckAgencyWithFlagOffNeverCallsTheAdapter(t *testing.T) {
	fake := sourcestest.Returning()
	w := &CheckAgencyWorker{
		logger:   discard,
		enabled:  flag(false),
		registry: sources.NewRegistry(sources.Deps{}, map[string]sources.Constructor{"la-metro": sourcestest.Constructor(fake)}),
	}
	if err := w.Work(t.Context(), checkJob("la-metro")); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("adapter called %d times with the flag off", len(calls))
	}
}

func TestCheckAgencyWithoutAdapterCompletes(t *testing.T) {
	w := &CheckAgencyWorker{logger: discard, enabled: flag(true), registry: sources.NewRegistry(sources.Deps{}, nil)}
	if err := w.Work(t.Context(), checkJob("octa")); err != nil {
		t.Fatalf("Work: %v", err)
	}
}

func TestCheckAgencyTimeoutAllowsSlowSites(t *testing.T) {
	if got := (&CheckAgencyWorker{}).Timeout(checkJob("x")); got != 10*time.Minute {
		t.Errorf("Timeout = %v, want 10m", got)
	}
}

func TestCheckWindowSpansThirtyDaysBackToNinetyAhead(t *testing.T) {
	pacific := time.FixedZone("PDT", -7*60*60)
	got := checkWindow(time.Date(2026, 11, 17, 6, 0, 0, 0, pacific))
	want := sources.Window{
		From: time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC),
		To:   time.Date(2027, 2, 15, 13, 0, 0, 0, time.UTC),
	}
	if got != want {
		t.Errorf("checkWindow = %+v, want %+v", got, want)
	}
}

func TestAdapterErrorRetriesOnlyRetryableErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		cancel bool
	}{
		{"server error", &fetch.Error{StatusCode: 502, Retryable: true, Err: errors.New("bad gateway")}, false},
		{"job timeout", context.DeadlineExceeded, false},
		{"not found", &fetch.Error{StatusCode: 404, Err: errors.New("not found")}, true},
		{"parse failure", errors.New("unexpected markup"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adapterError("la-metro", tc.err)
			var cancel *river.JobCancelError
			if errors.As(got, &cancel) != tc.cancel {
				t.Errorf("adapterError(%v) = %v, cancel = %v", tc.err, got, tc.cancel)
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("adapterError(%v) does not wrap the cause", tc.err)
			}
		})
	}
}

func TestCheckAgencyArgs(t *testing.T) {
	args := CheckAgencyArgs{AgencySlug: "la-metro"}
	if kind := args.Kind(); kind != "check_agency" {
		t.Errorf("Kind() = %q, want check_agency", kind)
	}
	opts := args.InsertOpts()
	want := river.UniqueOpts{ByArgs: true, ByPeriod: 24 * time.Hour, ByQueue: true}
	if opts.MaxAttempts != 5 || opts.UniqueOpts.ByArgs != want.ByArgs || opts.UniqueOpts.ByPeriod != want.ByPeriod || opts.UniqueOpts.ByQueue != want.ByQueue {
		t.Errorf("InsertOpts() = %+v", opts)
	}
}

func TestEnqueueAgencyChecksArgs(t *testing.T) {
	args := EnqueueAgencyChecksArgs{}
	if kind := args.Kind(); kind != "enqueue_agency_checks" {
		t.Errorf("Kind() = %q", kind)
	}
	opts := args.InsertOpts()
	if !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || opts.MaxAttempts != 5 {
		t.Errorf("InsertOpts() = %+v", opts)
	}
}

func TestEnqueueAgencyChecksWithFlagOffEnqueuesNothing(t *testing.T) {
	w := &EnqueueAgencyChecksWorker{logger: discard, enabled: flag(false)}
	job := &river.Job[EnqueueAgencyChecksArgs]{JobRow: &rivertype.JobRow{ID: 2}}
	if err := w.Work(t.Context(), job); err != nil {
		t.Fatalf("Work: %v", err)
	}
}

func TestDailyScheduleRunsOnceADayAtTheHour(t *testing.T) {
	s := dailySchedule{hourUTC: 13}
	cases := []struct {
		now, last, next time.Time
	}{
		{
			time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC),
			time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC),
			time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC),
		},
		{
			time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC),
			time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC),
			time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC),
		},
		{
			time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC),
			time.Date(2026, 12, 31, 13, 0, 0, 0, time.UTC),
			time.Date(2027, 1, 1, 13, 0, 0, 0, time.UTC),
		},
		{
			time.Date(2026, 11, 17, 7, 0, 0, 0, time.FixedZone("PST", -8*60*60)),
			time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC),
			time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		if got := s.last(tc.now); !got.Equal(tc.last) || got.Location() != time.UTC {
			t.Errorf("last(%v) = %v, want %v", tc.now, got, tc.last)
		}
		if got := s.Next(tc.now); !got.Equal(tc.next) {
			t.Errorf("Next(%v) = %v, want %v", tc.now, got, tc.next)
		}
	}
}

func TestPeriodicChecksTargetTheCurrentSlot(t *testing.T) {
	clock := sourcestest.NewClock(time.Date(2026, 11, 17, 12, 59, 59, 950_000_000, time.UTC))
	construct := periodicChecks(dailySchedule{hourUTC: 13}, clock, "ingest")

	args, opts := construct()
	want := EnqueueAgencyChecksArgs{Slot: time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)}
	if args != want || opts.Queue != "ingest" {
		t.Errorf("early by a few ms: args = %+v, opts = %+v", args, opts)
	}

	clock.Set(time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC))
	if args, _ := construct(); args != want {
		t.Errorf("after midnight before the hour: args = %+v, want the previous slot %+v", args, want)
	}
}

func TestNewClientAcceptsZeroConfig(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://127.0.0.1:1/civic?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := NewClient(pool, discard, Config{}); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := NewClient(pool, discard, Config{Queue: "Not A Queue"}); err == nil {
		t.Fatal("NewClient accepted an invalid queue name")
	}
}
