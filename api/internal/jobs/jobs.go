// Package jobs defines the River background jobs and client wiring.
package jobs

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// CheckAgencyArgs asks a worker to check one agency for new meetings.
type CheckAgencyArgs struct {
	AgencySlug string `json:"agency_slug"`
}

// Kind is the stable job name stored in the river_job table.
func (CheckAgencyArgs) Kind() string { return "check_agency" }

// CheckAgencyWorker is a placeholder; it only logs.
type CheckAgencyWorker struct {
	river.WorkerDefaults[CheckAgencyArgs]

	logger *slog.Logger
}

// Work handles one check_agency job.
func (w *CheckAgencyWorker) Work(ctx context.Context, job *river.Job[CheckAgencyArgs]) error {
	w.logger.InfoContext(ctx, "check agency (no-op)", "agency_slug", job.Args.AgencySlug, "job_id", job.ID)
	return nil
}

// NewClient returns a River client that works the default queue. Call Start to
// begin processing and Stop to drain.
func NewClient(pool *pgxpool.Pool, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &CheckAgencyWorker{logger: logger}); err != nil {
		return nil, err
	}

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:  logger,
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: workers,
		// Example daily check, once real agencies exist:
		// PeriodicJobs: []*river.PeriodicJob{river.NewPeriodicJob(
		// 	river.PeriodicInterval(24*time.Hour),
		// 	func() (river.JobArgs, *river.InsertOpts) { return CheckAgencyArgs{AgencySlug: "la-metro"}, nil },
		// 	nil,
		// )},
	})
}
