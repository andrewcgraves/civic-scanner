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
	Queue          string
	NoPeriodicJobs bool
}

func (c Config) withDefaults(st *store.Store) Config {
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
			EnqueueDownloadAudio{Queue: c.AudioQueue, Store: st, Enabled: c.Enabled},
		}
	}
	if c.AudioTimeout <= 0 {
		c.AudioTimeout = defaultAudioTimeout
	}
	if c.AudioFollowUp == nil {
		c.AudioFollowUp = NoAudioFollowUp{}
	}
	if c.Extractor == nil {
		c.Extractor = extract.New(config.Extract{})
	}
	if c.AgendaFollowUp == nil {
		c.AgendaFollowUp = NoAgendaFollowUp{}
	}
	return c
}

func NewClient(pool *pgxpool.Pool, logger *slog.Logger, cfg Config) (*river.Client[pgx.Tx], error) {
	st := store.New(pool)
	cfg = cfg.withDefaults(st)

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
		Logger: logger,
		Queues: map[string]river.QueueConfig{
			cfg.Queue:      {MaxWorkers: 10},
			cfg.AudioQueue: {MaxWorkers: audioQueueWorkers},
		},
		Workers:              workers,
		PeriodicJobs:         periodic,
		RescueStuckJobsAfter: max(rescueStuckJobsAfter, 2*(cfg.AudioTimeout+audioTimeoutHeadroom)),
	})
}
