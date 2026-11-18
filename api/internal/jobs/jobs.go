package jobs

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const FlagCheckAgencies = "ingest-check-agencies"

type Config struct {
	Registry       *sources.Registry
	Fetcher        fetch.Fetcher
	Clock          sources.Clock
	CheckHourUTC   int
	Enabled        func(ctx context.Context, flag string) bool
	FollowUp       FollowUp
	AgendaFollowUp AgendaFollowUp
	Queue          string
	NoPeriodicJobs bool
}

func (c Config) withDefaults() Config {
	if c.Clock == nil {
		c.Clock = sources.SystemClock{}
	}
	if c.Enabled == nil {
		c.Enabled = featureflags.Enabled
	}
	if c.Queue == "" {
		c.Queue = river.QueueDefault
	}
	if c.FollowUp == nil {
		c.FollowUp = EnqueueProcessAgenda{Queue: c.Queue}
	}
	if c.AgendaFollowUp == nil {
		c.AgendaFollowUp = NoAgendaFollowUp{}
	}
	return c
}

func NewClient(pool *pgxpool.Pool, logger *slog.Logger, cfg Config) (*river.Client[pgx.Tx], error) {
	cfg = cfg.withDefaults()
	st := store.New(pool)

	workers := river.NewWorkers()
	river.AddWorker(workers, &CheckAgencyWorker{
		logger:   logger,
		pool:     pool,
		store:    st,
		registry: cfg.Registry,
		clock:    cfg.Clock,
		enabled:  cfg.Enabled,
		followUp: cfg.FollowUp,
	})
	river.AddWorker(workers, &ProcessAgendaWorker{
		logger:   logger,
		pool:     pool,
		store:    st,
		fetcher:  cfg.Fetcher,
		enabled:  cfg.Enabled,
		followUp: cfg.AgendaFollowUp,
	})
	river.AddWorker(workers, &EnqueueAgencyChecksWorker{
		logger:  logger,
		store:   st,
		enabled: cfg.Enabled,
		queue:   cfg.Queue,
	})

	var periodic []*river.PeriodicJob
	if !cfg.NoPeriodicJobs {
		schedule := dailySchedule{hourUTC: cfg.CheckHourUTC}
		periodic = append(periodic, river.NewPeriodicJob(
			schedule,
			periodicChecks(schedule, cfg.Clock, cfg.Queue),
			&river.PeriodicJobOpts{ID: EnqueueAgencyChecksArgs{}.Kind(), RunOnStart: true},
		))
	}

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:       logger,
		Queues:       map[string]river.QueueConfig{cfg.Queue: {MaxWorkers: 10}},
		Workers:      workers,
		PeriodicJobs: periodic,
	})
}
