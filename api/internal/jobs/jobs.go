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
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
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
	Storage        storage.Store
	Audio          AudioDownloader
	AudioTimeout   time.Duration
	AudioFollowUp  AudioFollowUp
	AudioQueue     string
	AudioTempDir   string
	Transcription  Transcription
	LLM            llm.Client
	SummaryTier    llm.Tier
	LLMTimeout     time.Duration
	Queue          string
	NoPeriodicJobs bool
}

func (c Config) withDefaults(logger *slog.Logger, st *store.Store) Config {
	if c.Clock == nil {
		c.Clock = sources.SystemClock{}
	}
	if c.Enabled == nil {
		c.Enabled = featureflags.Enabled
	}
	if c.Queue == "" {
		c.Queue = river.QueueDefault
	}
	if c.AudioQueue == "" {
		c.AudioQueue = AudioQueue
	}
	if c.FollowUp == nil {
		c.FollowUp = FollowUps{
			EnqueueProcessAgenda{Queue: c.Queue},
			EnqueueDownloadAudio{Queue: c.AudioQueue, Store: st, Clock: c.Clock, Enabled: c.Enabled},
		}
	}
	if c.AudioTimeout <= 0 {
		c.AudioTimeout = defaultAudioTimeout
	}
	if c.Storage == nil {
		c.Storage = unconfiguredStorage()
	}
	if c.AudioFollowUp == nil {
		c.AudioFollowUp = EnqueueTranscribeAudio{Queue: c.AudioQueue, Enabled: c.Enabled, Logger: logger}
	}
	c.Transcription = c.Transcription.withDefaults()
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
	st := store.New(pool)
	cfg = cfg.withDefaults(logger, st)

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
	})
	river.AddWorker(workers, &DownloadAudioWorker{
		logger:     logger,
		pool:       pool,
		store:      st,
		storage:    cfg.Storage,
		downloader: cfg.Audio,
		clock:      cfg.Clock,
		enabled:    cfg.Enabled,
		followUp:   cfg.AudioFollowUp,
		timeout:    cfg.AudioTimeout,
		tempDir:    cfg.AudioTempDir,
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
	river.AddWorker(workers, &TranscribeAudioWorker{
		logger:   logger,
		pool:     pool,
		store:    st,
		storage:  cfg.Storage,
		settings: cfg.Transcription,
		enabled:  cfg.Enabled,
		tempDir:  cfg.AudioTempDir,
	})
	river.AddWorker(workers, &EnqueueTranscriptionsWorker{
		logger:      logger,
		store:       st,
		transcriber: cfg.Transcription.Client,
		enabled:     cfg.Enabled,
		queue:       cfg.AudioQueue,
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
		))
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(transcriptSweepInterval),
			periodicTranscriptSweep(cfg.Clock, cfg.Queue),
			&river.PeriodicJobOpts{ID: EnqueueTranscriptionsArgs{}.Kind(), RunOnStart: true},
		))
	}

	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:                      logger,
		CancelledJobRetentionPeriod: summaryFailureBackoff,
		DiscardedJobRetentionPeriod: summaryFailureBackoff,
		Queues: map[string]river.QueueConfig{
			cfg.Queue:      {MaxWorkers: 10},
			cfg.AudioQueue: {MaxWorkers: audioQueueWorkers},
		},
		Workers:      workers,
		PeriodicJobs: periodic,
	})
}

func unconfiguredStorage() storage.Store {
	store, _ := storage.New(config.Storage{})
	return store
}
