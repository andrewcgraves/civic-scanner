package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	AudioTempPrefix       = "civic-audio-"
	audioTempFileName     = "audio.mp3"
	audioKeyPrefix        = "recordings/"
	audioKeyFileExtension = ".mp3"
	dailyRetry            = 12 * time.Hour
	weeklyRetry           = 6*24*time.Hour + 12*time.Hour
	failureWriteTimeout   = 10 * time.Second
)

var ErrNoDownloader = errors.New("jobs: no audio downloader configured")

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

func PermanentFailure(kind string) bool {
	switch kind {
	case media.KindUnsupported, media.KindNoMedia, media.KindNoAudio, media.KindMalformed:
		return true
	}
	return false
}

func retryAfter(kind string, now time.Time) *time.Time {
	var wait time.Duration
	switch {
	case PermanentFailure(kind):
		return nil
	case kind == media.KindRefused || kind == media.KindLimit:
		wait = weeklyRetry
	default:
		wait = dailyRetry
	}
	at := now.Add(wait)
	return &at
}

func waiting(rec store.Recording, now time.Time) (string, bool) {
	switch {
	case PermanentFailure(rec.Download.Failure):
		return "permanent failure " + rec.Download.Failure, true
	case rec.Download.RetryAfter != nil && now.Before(*rec.Download.RetryAfter):
		return "backing off after " + rec.Download.Failure, true
	}
	return "", false
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
	Clock   sources.Clock
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
	if _, wait := waiting(rec, e.now()); wait {
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

func (e EnqueueDownloadAudio) now() time.Time {
	if e.Clock == nil {
		return time.Now()
	}
	return e.Clock.Now()
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

func SweepAudioTemp(dir string, olderThan time.Duration, now time.Time) (int, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("sweep audio temp: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), AudioTempPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < olderThan {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return removed, fmt.Errorf("sweep audio temp: %w", err)
		}
		removed++
	}
	return removed, nil
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
	if w.skip(ctx, logger, rec) {
		return nil
	}

	key := AudioKey(rec.ID)
	uploaded, err := w.uploaded(ctx, key)
	if err != nil {
		return storageError(key, err)
	}
	if uploaded {
		logger.InfoContext(ctx, "audio already in the bucket, recording it", "key", key)
	} else if err := w.download(ctx, logger, job, rec, key); err != nil {
		return err
	}
	return w.record(ctx, logger, rec.ID, key)
}

func (w *DownloadAudioWorker) skip(ctx context.Context, logger *slog.Logger, rec store.Recording) bool {
	if rec.Audio != nil {
		logger.InfoContext(ctx, "download audio skipped: audio already stored", "key", rec.Audio.Key)
		return true
	}
	if rec.AudioDeletedAt != nil {
		logger.InfoContext(ctx, "download audio skipped: audio was deleted by retention", "deleted_at", rec.AudioDeletedAt)
		return true
	}
	if reason, wait := waiting(rec, w.clock.Now()); wait {
		logger.InfoContext(ctx, "download audio skipped: "+reason, "retry_after", rec.Download.RetryAfter,
			"attempts", rec.Download.Attempts)
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

func (w *DownloadAudioWorker) download(ctx context.Context, logger *slog.Logger, job *river.Job[DownloadAudioArgs], rec store.Recording, key string) error {
	dir, err := os.MkdirTemp(w.tempDir, AudioTempPrefix+"*")
	if err != nil {
		return fmt.Errorf("audio temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	dst := filepath.Join(dir, audioTempFileName)
	audio, err := w.downloader.DownloadAudio(ctx, rec.SourceURL, dst)
	if err != nil {
		return w.failed(ctx, logger, job, rec, err)
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

func (w *DownloadAudioWorker) failed(ctx context.Context, logger *slog.Logger, job *river.Job[DownloadAudioArgs], rec store.Recording, err error) error {
	kind := media.FailureKind(err)
	err = fmt.Errorf("recording %s: %w", rec.SourceURL, err)
	retryable := media.IsRetryable(err)
	logger.InfoContext(ctx, "audio not downloaded", "error", err, "kind", kind, "retryable", retryable)
	if retryable && job.Attempt < job.MaxAttempts {
		return err
	}
	failure := store.DownloadFailure{Kind: kind, RetryAfter: retryAfter(kind, w.clock.Now())}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failureWriteTimeout)
	defer cancel()
	if _, recErr := w.store.RecordDownloadFailure(writeCtx, rec.ID, failure); recErr != nil {
		logger.WarnContext(ctx, "download failure not recorded", "error", recErr)
	}
	if retryable {
		return err
	}
	return river.JobCancel(err)
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

func storageError(key string, err error) error {
	err = fmt.Errorf("audio %s: %w", key, err)
	if storage.IsRetryable(err) || sources.Retryable(err) {
		return err
	}
	return river.JobCancel(err)
}
