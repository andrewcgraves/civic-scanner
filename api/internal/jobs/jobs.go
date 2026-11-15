package jobs

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type CheckAgencyArgs struct {
	AgencySlug string `json:"agency_slug"`
}

func (CheckAgencyArgs) Kind() string { return "check_agency" }

type CheckAgencyWorker struct {
	river.WorkerDefaults[CheckAgencyArgs]

	logger *slog.Logger
}

func (w *CheckAgencyWorker) Work(ctx context.Context, job *river.Job[CheckAgencyArgs]) error {
	w.logger.InfoContext(ctx, "check agency (no-op)", "agency_slug", job.Args.AgencySlug, "job_id", job.ID)
	return nil
}

func NewClient(pool *pgxpool.Pool, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	if err := river.AddWorkerSafely(workers, &CheckAgencyWorker{logger: logger}); err != nil {
		return nil, err
	}

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:  logger,
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: workers,
	})
}
