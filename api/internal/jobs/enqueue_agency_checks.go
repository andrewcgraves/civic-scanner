package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const scheduleSlack = time.Minute

type EnqueueAgencyChecksArgs struct {
	Slot time.Time `json:"slot"`
}

func (EnqueueAgencyChecksArgs) Kind() string { return "enqueue_agency_checks" }

func (EnqueueAgencyChecksArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type EnqueueAgencyChecksWorker struct {
	river.WorkerDefaults[EnqueueAgencyChecksArgs]

	logger  *slog.Logger
	store   *store.Store
	enabled func(context.Context, string) bool
	queue   string
}

func (w *EnqueueAgencyChecksWorker) Work(ctx context.Context, job *river.Job[EnqueueAgencyChecksArgs]) error {
	logger := w.logger.With("job_id", job.ID, "slot", job.Args.Slot)
	if !w.enabled(ctx, FlagCheckAgencies) {
		logger.InfoContext(ctx, "agency checks not enqueued: flag off", "flag", FlagCheckAgencies)
		return nil
	}
	agencies, err := w.store.ListAgencies(ctx)
	if err != nil {
		return err
	}
	if len(agencies) == 0 {
		logger.InfoContext(ctx, "agency checks not enqueued: no live agencies")
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}

	params := make([]river.InsertManyParams, 0, len(agencies))
	for _, a := range agencies {
		params = append(params, river.InsertManyParams{
			Args:       CheckAgencyArgs{AgencySlug: a.Slug},
			InsertOpts: &river.InsertOpts{Queue: w.queue, ScheduledAt: job.Args.Slot},
		})
	}
	results, err := client.InsertMany(ctx, params)
	if err != nil {
		return fmt.Errorf("enqueue agency checks: %w", err)
	}
	skipped := 0
	for _, r := range results {
		if r.UniqueSkippedAsDuplicate {
			skipped++
		}
	}
	logger.InfoContext(ctx, "agency checks enqueued", "agencies", len(agencies), "inserted", len(results)-skipped, "duplicates", skipped)
	return nil
}

type dailySchedule struct {
	hourUTC int
}

func (s dailySchedule) last(t time.Time) time.Time {
	t = t.UTC()
	slot := time.Date(t.Year(), t.Month(), t.Day(), s.hourUTC, 0, 0, 0, time.UTC)
	if slot.After(t) {
		slot = slot.AddDate(0, 0, -1)
	}
	return slot
}

func (s dailySchedule) Next(t time.Time) time.Time {
	return s.last(t).AddDate(0, 0, 1)
}

func periodicChecks(schedule dailySchedule, clock sources.Clock, queue string) river.PeriodicJobConstructor {
	return func() (river.JobArgs, *river.InsertOpts) {
		return EnqueueAgencyChecksArgs{Slot: schedule.last(clock.Now().Add(scheduleSlack))}, &river.InsertOpts{Queue: queue}
	}
}
