package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	FlagAudioRetention       = "ingest-audio-retention"
	audioRetentionHourUTC    = 10
	audioRetentionBatchSize  = 100
	audioRetentionMaxBatches = 10
	audioRetentionTimeout    = 15 * time.Minute
)

type DeleteExpiredAudioArgs struct {
	Slot time.Time `json:"slot"`
}

func (DeleteExpiredAudioArgs) Kind() string { return "delete_expired_audio" }

func (DeleteExpiredAudioArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type audioRetentionStore interface {
	ListTranscribedRecordingsWithExpiredAudio(ctx context.Context, now time.Time, limit int) ([]store.Recording, error)
	MarkRecordingAudioDeleted(ctx context.Context, recordingID uuid.UUID, key string) (store.Recording, error)
}

type DeleteExpiredAudioWorker struct {
	river.WorkerDefaults[DeleteExpiredAudioArgs]

	logger  *slog.Logger
	store   audioRetentionStore
	storage storage.Store
	clock   sources.Clock
	enabled func(context.Context, string) bool
}

func (w *DeleteExpiredAudioWorker) Timeout(*river.Job[DeleteExpiredAudioArgs]) time.Duration {
	return audioRetentionTimeout
}

func (w *DeleteExpiredAudioWorker) Work(ctx context.Context, job *river.Job[DeleteExpiredAudioArgs]) error {
	logger := w.logger.With("job_id", job.ID, "slot", job.Args.Slot)
	if !w.enabled(ctx, FlagAudioRetention) {
		logger.InfoContext(ctx, "expired audio not deleted: flag off", "flag", FlagAudioRetention)
		return nil
	}
	now := w.clock.Now()
	deleted := 0
	for range audioRetentionMaxBatches {
		recordings, err := w.store.ListTranscribedRecordingsWithExpiredAudio(ctx, now, audioRetentionBatchSize)
		if err != nil {
			return err
		}
		for _, rec := range recordings {
			if err := w.delete(ctx, logger, rec); err != nil {
				logger.WarnContext(ctx, "expired audio not deleted; River retries the job", "recording_id", rec.ID,
					"error", err, "deleted", deleted)
				return err
			}
			deleted++
		}
		if len(recordings) < audioRetentionBatchSize {
			logger.InfoContext(ctx, "expired audio deleted", "deleted", deleted, "now", now)
			return nil
		}
	}
	logger.InfoContext(ctx, "expired audio deleted; more remain for the next run", "deleted", deleted, "now", now,
		"limit", audioRetentionBatchSize*audioRetentionMaxBatches)
	return nil
}

func (w *DeleteExpiredAudioWorker) delete(ctx context.Context, logger *slog.Logger, rec store.Recording) error {
	key := rec.Audio.Key
	logger = logger.With("recording_id", rec.ID, "key", key, "expires_at", rec.Audio.ExpiresAt)
	if err := w.storage.Delete(ctx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return storageError(key, err)
	}
	_, err := w.store.MarkRecordingAudioDeleted(ctx, rec.ID, key)
	switch {
	case err == nil:
		logger.InfoContext(ctx, "audio deleted by retention")
		return nil
	case errors.Is(err, store.ErrAudioChanged), errors.Is(err, store.ErrNotFound):
		logger.InfoContext(ctx, "audio deleted by retention; the recording changed meanwhile", "error", err)
		return nil
	}
	return fmt.Errorf("mark audio of recording %s deleted: %w", rec.ID, err)
}

func periodicAudioRetention(schedule dailySchedule, clock sources.Clock, queue string) river.PeriodicJobConstructor {
	return func() (river.JobArgs, *river.InsertOpts) {
		return DeleteExpiredAudioArgs{Slot: schedule.last(clock.Now().Add(scheduleSlack))}, &river.InsertOpts{Queue: queue}
	}
}
