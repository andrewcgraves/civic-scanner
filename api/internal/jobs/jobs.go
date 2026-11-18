package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const FlagCheckAgencies = "ingest-check-agencies"

type Config struct {
	Registry       *sources.Registry
	Fetcher        fetch.Fetcher
	Fallback       fetch.Fetcher
	Extractor      TextExtractor
	Clock          sources.Clock
	CheckHourUTC   int
	Enabled        func(ctx context.Context, flag string) bool
	FollowUp       FollowUp
	AgendaFollowUp AgendaFollowUp
	LLM            llm.Client
	SummaryTier    llm.Tier
	LLMTimeout     time.Duration
	Queue          string
	NoPeriodicJobs bool
}

func (c Config) withDefaults(logger *slog.Logger) Config {
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
	if c.Extractor == nil {
		c.Extractor = extract.New(config.Extract{})
	}
	if c.AgendaFollowUp == nil {
		c.AgendaFollowUp = EnqueueSummarizeAgenda{Queue: c.Queue, Logger: logger}
	}
	if c.SummaryTier == "" {
		c.SummaryTier = llm.TierRoutine
	}
	if c.LLMTimeout <= 0 {
		c.LLMTimeout = defaultLLMTimeout
	}
	return c
}

func NewClient(pool *pgxpool.Pool, logger *slog.Logger, cfg Config) (*river.Client[pgx.Tx], error) {
	cfg = cfg.withDefaults(logger)
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
		logger:    logger,
		pool:      pool,
		store:     st,
		fetcher:   cfg.Fetcher,
		fallback:  cfg.Fallback,
		extractor: cfg.Extractor,
		enabled:   cfg.Enabled,
		followUp:  cfg.AgendaFollowUp,
		queue:     cfg.Queue,
	})
	river.AddWorker(workers, &SummarizeAgendaWorker{
		logger:     logger,
		store:      st,
		llm:        cfg.LLM,
		tier:       cfg.SummaryTier,
		llmTimeout: cfg.LLMTimeout,
		enabled:    cfg.Enabled,
	})
	river.AddWorker(workers, &EnqueueAgendaSummariesWorker{
		logger:  logger,
		store:   st,
		clock:   cfg.Clock,
		llm:     cfg.LLM,
		enabled: cfg.Enabled,
		queue:   cfg.Queue,
	})
	river.AddWorker(workers, &DescribeAgendaChangeWorker{
		logger:     logger,
		store:      st,
		llm:        cfg.LLM,
		tier:       cfg.SummaryTier,
		llmTimeout: cfg.LLMTimeout,
		enabled:    cfg.Enabled,
	})
	river.AddWorker(workers, &EnqueueAgendaChangesWorker{
		logger: logger,
		store:  st,
		clock:  cfg.Clock,
		queue:  cfg.Queue,
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
		), river.NewPeriodicJob(
			river.PeriodicInterval(summarySweepInterval),
			periodicSummarySweep(cfg.Clock, cfg.Queue),
			&river.PeriodicJobOpts{ID: EnqueueAgendaSummariesArgs{}.Kind(), RunOnStart: true},
		), river.NewPeriodicJob(
			river.PeriodicInterval(changeSweepInterval),
			periodicChangeSweep(cfg.Clock, cfg.Queue),
			&river.PeriodicJobOpts{ID: EnqueueAgendaChangesArgs{}.Kind(), RunOnStart: true},
		))
	}

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:                      logger,
		CancelledJobRetentionPeriod: summaryFailureBackoff,
		DiscardedJobRetentionPeriod: summaryFailureBackoff,
		Queues:                      map[string]river.QueueConfig{cfg.Queue: {MaxWorkers: 10}},
		Workers:                     workers,
		PeriodicJobs:                periodic,
	})
}
