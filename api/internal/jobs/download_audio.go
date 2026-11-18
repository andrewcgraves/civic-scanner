package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/media"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	FlagDownloadAudio     = "ingest-download-audio"
	AudioQueue            = "audio"
	audioQueueWorkers     = 1
	audioRetentionMonths  = 6
	defaultAudioTimeout   = time.Hour
	audioTimeoutHeadroom  = 15 * time.Minute
	rescueStuckJobsAfter  = 3 * time.Hour
	audioTempPattern      = "civic-audio-*"
	audioTempFileName     = "audio.mp3"
	audioKeyPrefix        = "recordings/"
	audioKeyFileExtension = ".mp3"
)

var (
	ErrNoStorage    = errors.New("jobs: no object storage configured")
	ErrNoDownloader = errors.New("jobs: no audio downloader configured")
)

type DownloadAudioArgs struct {
	RecordingID uuid.UUID `json:"recording_id"`
}

func (DownloadAudioArgs) Kind() string { return "download_audio" }

func (DownloadAudioArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByPeriod: 24 * time.Hour, ByQueue: true},
	}
}

func AudioKey(recordingID uuid.UUID) string {
	return audioKeyPrefix + recordingID.String() + audioKeyFileExtension
}

type AudioDownloader interface {
	DownloadAudio(ctx context.Context, recordingURL, dst string) (media.Audio, error)
}

type AudioFollowUp interface {
	AudioStored(ctx context.Context, tx pgx.Tx, r store.Recording) error
}

type NoAudioFollowUp struct{}

func (NoAudioFollowUp) AudioStored(context.Context, pgx.Tx, store.Recording) error { return nil }

type EnqueueDownloadAudio struct {
	Queue   string
	Store   *store.Store
	Enabled func(ctx context.Context, flag string) bool
}

func (e EnqueueDownloadAudio) MeetingUpserted(ctx context.Context, tx pgx.Tx, m UpsertedMeeting) error {
	if m.RecordingURL == "" || m.Meeting.Status != string(sources.StatusHeld) {
		return nil
	}
	if e.Enabled == nil || !e.Enabled(ctx, FlagDownloadAudio) {
		return nil
	}
	rec, _, err := e.Store.UpsertRecordingTx(ctx, tx, store.RecordingInput{MeetingID: m.Meeting.ID, SourceURL: m.RecordingURL})
	if err != nil {
		return err
	}
	if rec.Audio != nil || rec.AudioDeletedAt != nil {
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	if _, err := client.InsertTx(ctx, tx, DownloadAudioArgs{RecordingID: rec.ID}, &river.InsertOpts{Queue: e.Queue}); err != nil {
		return fmt.Errorf("enqueue download audio: %w", err)
	}
	return nil
}

type FollowUps []FollowUp

func (f FollowUps) MeetingUpserted(ctx context.Context, tx pgx.Tx, m UpsertedMeeting) error {
	for _, followUp := range f {
		if err := followUp.MeetingUpserted(ctx, tx, m); err != nil {
			return err
		}
	}
	return nil
}

type DownloadAudioWorker struct {
	river.WorkerDefaults[DownloadAudioArgs]

	logger     *slog.Logger
	pool       *pgxpool.Pool
	store      *store.Store
	storage    storage.Store
	downloader AudioDownloader
	clock      sources.Clock
	enabled    func(context.Context, string) bool
	followUp   AudioFollowUp
	timeout    time.Duration
	tempDir    string
}

func (w *DownloadAudioWorker) Timeout(*river.Job[DownloadAudioArgs]) time.Duration {
	return w.timeout + audioTimeoutHeadroom
}

func (w *DownloadAudioWorker) Work(ctx context.Context, job *river.Job[DownloadAudioArgs]) error {
	logger := w.logger.With("recording_id", job.Args.RecordingID, "job_id", job.ID)
	if !w.enabled(ctx, FlagDownloadAudio) {
		logger.InfoContext(ctx, "download audio skipped: flag off", "flag", FlagDownloadAudio)
		return nil
	}
	if w.storage == nil {
		return river.JobCancel(ErrNoStorage)
	}
	if w.downloader == nil {
		return river.JobCancel(ErrNoDownloader)
	}
	rec, err := w.store.GetRecording(ctx, job.Args.RecordingID)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(fmt.Errorf("recording %s: %w", job.Args.RecordingID, err))
	}
	if err != nil {
		return err
	}
	logger = logger.With("recording_url", rec.SourceURL)
	if done := w.skip(ctx, logger, rec); done {
		return nil
	}

	key := AudioKey(rec.ID)
	uploaded, err := w.uploaded(ctx, key)
	if err != nil {
		return storageError(key, err)
	}
	if uploaded {
		logger.InfoContext(ctx, "audio already in the bucket, recording it", "key", key)
	} else if err := w.download(ctx, logger, rec, key); err != nil {
		return err
	}
	return w.record(ctx, logger, rec.ID, key)
}

func (w *DownloadAudioWorker) skip(ctx context.Context, logger *slog.Logger, rec store.Recording) bool {
	switch {
	case rec.Audio != nil:
		logger.InfoContext(ctx, "download audio skipped: audio already stored", "key", rec.Audio.Key)
		return true
	case rec.AudioDeletedAt != nil:
		logger.InfoContext(ctx, "download audio skipped: audio was deleted by retention", "deleted_at", rec.AudioDeletedAt)
		return true
	}
	return false
}

func (w *DownloadAudioWorker) uploaded(ctx context.Context, key string) (bool, error) {
	info, err := w.storage.Stat(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Size > 0, nil
}

func (w *DownloadAudioWorker) download(ctx context.Context, logger *slog.Logger, rec store.Recording, key string) error {
	dir, err := os.MkdirTemp(w.tempDir, audioTempPattern)
	if err != nil {
		return fmt.Errorf("audio temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	dst := filepath.Join(dir, audioTempFileName)
	audio, err := w.downloader.DownloadAudio(ctx, rec.SourceURL, dst)
	if err != nil {
		logger.InfoContext(ctx, "audio not downloaded", "error", err)
		return mediaError(rec.SourceURL, err)
	}
	f, err := os.Open(dst) //nolint:gosec
	if err != nil {
		return fmt.Errorf("open downloaded audio: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := w.storage.Put(ctx, key, f); err != nil {
		return storageError(key, err)
	}
	logger.InfoContext(ctx, "audio uploaded", "key", key, "bytes", audio.Bytes, "via", audio.Via, "media_url", audio.MediaURL)
	return nil
}

func (w *DownloadAudioWorker) record(ctx context.Context, logger *slog.Logger, recordingID uuid.UUID, key string) error {
	audio := store.RecordingAudio{Key: key, ExpiresAt: w.clock.Now().UTC().AddDate(0, audioRetentionMonths, 0)}
	err := pgx.BeginTxFunc(ctx, w.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		rec, err := w.store.AddRecordingAudioTx(ctx, tx, recordingID, audio)
		if err != nil {
			return err
		}
		if err := w.followUp.AudioStored(ctx, tx, rec); err != nil {
			return fmt.Errorf("follow up audio of recording %s: %w", recordingID, err)
		}
		return nil
	})
	switch {
	case err == nil:
		logger.InfoContext(ctx, "audio stored", "key", key, "expires_at", audio.ExpiresAt)
		return nil
	case errors.Is(err, store.ErrAudioChanged):
		return w.resolveConflict(ctx, logger, recordingID, key)
	case errors.Is(err, store.ErrNotFound):
		return river.JobCancel(err)
	}
	return err
}

func (w *DownloadAudioWorker) resolveConflict(ctx context.Context, logger *slog.Logger, recordingID uuid.UUID, key string) error {
	rec, err := w.store.GetRecording(ctx, recordingID)
	if err != nil {
		return err
	}
	if rec.AudioDeletedAt == nil {
		logger.InfoContext(ctx, "audio was stored by another run", "key", rec.Audio.Key)
		return nil
	}
	if err := w.storage.Delete(ctx, key); err != nil {
		return storageError(key, err)
	}
	logger.InfoContext(ctx, "audio deleted by retention while downloading; removed the upload", "key", key)
	return nil
}

func mediaError(recordingURL string, err error) error {
	err = fmt.Errorf("recording %s: %w", recordingURL, err)
	if media.IsRetryable(err) {
		return err
	}
	return river.JobCancel(err)
}

func storageError(key string, err error) error {
	err = fmt.Errorf("audio %s: %w", key, err)
	if storage.IsRetryable(err) || sources.Retryable(err) {
		return err
	}
	return river.JobCancel(err)
}
