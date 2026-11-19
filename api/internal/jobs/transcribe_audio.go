package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/media"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/summarize"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

const (
	FlagTranscribe               = "ingest-transcribe"
	TranscribeTempPrefix         = AudioTempPrefix + "transcribe-"
	transcribeMaxAttempts        = 3
	defaultTranscribeTimeout     = 2 * time.Hour
	defaultTranscribeMaxDuration = 8 * time.Hour
	defaultTranscribeMaxBytes    = 128 << 20
	transcribeSaveTimeout        = 30 * time.Second
	transcribeAudioFileName      = "audio.mp3"
	MaxSegmentBytes              = 4000
	MaxSpeakerBytes              = 64
	MaxTranscriptBytes           = 1 << 20
	MaxTranscriptSegments        = 10000
)

const (
	TranscribeKindLimit        = "limit"
	TranscribeKindUnavailable  = "unavailable"
	TranscribeKindMalformed    = "malformed"
	TranscribeKindRefused      = "refused"
	TranscribeKindTooLarge     = "too_large"
	TranscribeKindEmpty        = "empty"
	TranscribeKindMissingAudio = "missing_audio"
	TranscribeKindTransient    = "transient"
	TranscribeKindFailed       = "failed"
)

var (
	ErrAudioTooLarge      = errors.New("jobs: stored audio is larger than the transcription limit")
	ErrTranscriptTooLarge = errors.New("jobs: transcript is larger than the storage limit")
	ErrEmptyTranscript    = errors.New("jobs: transcription returned no text")
	ErrAudioMissing       = errors.New("jobs: recording has audio but the bucket has no object")
	errAudioReadShort     = errors.New("jobs: audio read from the bucket does not match its size")
)

type Transcription struct {
	Client          transcribe.Client
	Model           string
	ProviderOptions string
	Timeout         time.Duration
	MaxDuration     time.Duration
	MaxAudioBytes   int64
	Concurrency     int
	ChunkRetries    int
	RetryDelay      time.Duration
	FollowUp        TranscriptFollowUp
}

func (t Transcription) withDefaults() Transcription {
	if t.Timeout <= 0 {
		t.Timeout = defaultTranscribeTimeout
	}
	if t.MaxDuration <= 0 {
		t.MaxDuration = defaultTranscribeMaxDuration
	}
	if t.MaxAudioBytes <= 0 {
		t.MaxAudioBytes = defaultTranscribeMaxBytes
	}
	if t.Concurrency <= 0 {
		t.Concurrency = 1
	}
	if t.FollowUp == nil {
		t.FollowUp = NoTranscriptFollowUp{}
	}
	return t
}

type TranscribeAudioArgs struct {
	RecordingID uuid.UUID `json:"recording_id"`
}

func (TranscribeAudioArgs) Kind() string { return "transcribe_audio" }

func (TranscribeAudioArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: transcribeMaxAttempts,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByQueue: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
			rivertype.JobStateRunning, rivertype.JobStateScheduled,
		}},
	}
}

type TranscriptFollowUp interface {
	TranscriptSaved(ctx context.Context, tx pgx.Tx, t store.Transcript) error
}

type NoTranscriptFollowUp struct{}

func (NoTranscriptFollowUp) TranscriptSaved(context.Context, pgx.Tx, store.Transcript) error {
	return nil
}

type EnqueueTranscribeAudio struct {
	Queue   string
	Enabled func(ctx context.Context, flag string) bool
	Logger  *slog.Logger
}

func (e EnqueueTranscribeAudio) AudioStored(ctx context.Context, tx pgx.Tx, r store.Recording) error {
	if e.Enabled == nil || !e.Enabled(ctx, FlagTranscribe) {
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err == nil {
		err = pgx.BeginFunc(ctx, tx, func(savepoint pgx.Tx) error {
			_, err := client.InsertTx(ctx, savepoint, TranscribeAudioArgs{RecordingID: r.ID}, &river.InsertOpts{Queue: e.Queue})
			return err
		})
	}
	if err != nil {
		logger := e.Logger
		if logger == nil {
			logger = slog.New(slog.DiscardHandler)
		}
		logger.WarnContext(ctx, "transcription not enqueued; the sweep will pick the recording up",
			"recording_id", r.ID, "meeting_id", r.MeetingID, "error", err)
	}
	return nil
}

type TranscribeAudioWorker struct {
	river.WorkerDefaults[TranscribeAudioArgs]

	logger   *slog.Logger
	pool     *pgxpool.Pool
	store    *store.Store
	storage  storage.Store
	settings Transcription
	enabled  func(context.Context, string) bool
	tempDir  string
}

func (w *TranscribeAudioWorker) Timeout(*river.Job[TranscribeAudioArgs]) time.Duration {
	return w.settings.Timeout
}

func (w *TranscribeAudioWorker) Work(ctx context.Context, job *river.Job[TranscribeAudioArgs]) error {
	id := job.Args.RecordingID
	logger := w.logger.With("recording_id", id, "job_id", job.ID, "attempt", job.Attempt)
	if !w.enabled(ctx, FlagTranscribe) {
		logger.InfoContext(ctx, "transcription skipped: flag off", "flag", FlagTranscribe)
		return nil
	}
	if w.settings.Client == nil {
		logger.InfoContext(ctx, "transcription skipped: no transcription client configured")
		return nil
	}
	rec, err := w.store.GetRecording(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(fmt.Errorf("recording %s: %w", id, err))
	}
	if err != nil {
		return err
	}
	if done, err := w.transcribed(ctx, id); err != nil || done {
		if done {
			logger.InfoContext(ctx, "transcription skipped: already transcribed")
		}
		return err
	}
	if rec.Audio == nil {
		logger.InfoContext(ctx, "transcription skipped: no stored audio", "audio_deleted_at", rec.AudioDeletedAt)
		return nil
	}
	key := rec.Audio.Key
	logger = logger.With("key", key)
	info, err := w.storage.Stat(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return w.audioGone(ctx, logger, id, key)
	}
	if err != nil {
		return storageError(key, err)
	}
	if info.Size > w.settings.MaxAudioBytes {
		return w.cancel(ctx, logger, id, fmt.Errorf("%w: %d bytes, limit %d", ErrAudioTooLarge, info.Size, w.settings.MaxAudioBytes))
	}

	dir, err := os.MkdirTemp(w.tempDir, TranscribeTempPrefix+"*")
	if err != nil {
		return fmt.Errorf("transcribe temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, transcribeAudioFileName)
	if err := w.fetch(ctx, key, info.Size, path); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return w.audioGone(ctx, logger, id, key)
		}
		return err
	}

	checkpoint := newChunkCheckpoint(w.storage, logger, TranscriptObjectsPrefix(id),
		ChunkCheckpointPrefix(id, info, CheckpointInputs{Model: w.settings.Model, ProviderOptions: w.settings.ProviderOptions}))
	started := time.Now()
	t, err := w.transcribe(ctx, path, dir, checkpoint)
	if errors.Is(err, transcribe.ErrNotConfigured) {
		logger.InfoContext(ctx, "transcription skipped: transcription not configured")
		return nil
	}
	if err != nil {
		return w.failed(ctx, logger, job, err)
	}
	in, err := TranscriptInput(id, t)
	if err != nil {
		return w.cancel(ctx, logger, id, err)
	}
	logger = logger.With("audio_bytes", info.Size, "duration", t.Duration, "segments", len(in.Segments), "text_bytes", len(in.Text),
		"took", time.Since(started).Round(time.Second), "chunks_reused", checkpoint.reused())
	return w.save(ctx, logger, in, checkpoint)
}

func (w *TranscribeAudioWorker) transcribed(ctx context.Context, id uuid.UUID) (bool, error) {
	_, err := w.store.GetTranscript(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (w *TranscribeAudioWorker) audioGone(ctx context.Context, logger *slog.Logger, id uuid.UUID, key string) error {
	rec, err := w.store.GetRecording(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(fmt.Errorf("recording %s: %w", id, err))
	}
	if err != nil {
		return err
	}
	if rec.Audio == nil || rec.Audio.Key != key {
		logger.InfoContext(ctx, "transcription skipped: audio deleted before it was read", "audio_deleted_at", rec.AudioDeletedAt)
		return nil
	}
	return w.cancel(ctx, logger, id, fmt.Errorf("%w: %s", ErrAudioMissing, key))
}

func (w *TranscribeAudioWorker) fetch(ctx context.Context, key string, size int64, dst string) error {
	body, err := w.storage.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return err
	}
	if err != nil {
		return storageError(key, err)
	}
	defer func() { _ = body.Close() }()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec
	if err != nil {
		return fmt.Errorf("create audio file: %w", err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(body, size+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("read audio %s: %w", key, copyErr)
	case closeErr != nil:
		return fmt.Errorf("write audio %s: %w", key, closeErr)
	case n != size:
		return fmt.Errorf("%w: %s read %d bytes, stat said %d", errAudioReadShort, key, n, size)
	}
	return nil
}

func (w *TranscribeAudioWorker) transcribe(ctx context.Context, path, dir string, checkpoint *chunkCheckpoint) (transcribe.Transcript, error) {
	client := w.settings.Client
	resumable, ok := client.(transcribe.Resumable)
	if !ok {
		return client.Transcribe(ctx, path)
	}
	return resumable.TranscribeWith(ctx, path, transcribe.Options{
		MaxDuration: w.settings.MaxDuration,
		Concurrency: w.settings.Concurrency,
		Retries:     w.settings.ChunkRetries,
		RetryDelay:  w.settings.RetryDelay,
		Checkpoint:  checkpoint,
		TempDir:     dir,
	})
}

func TranscribeFailureKind(err error) string {
	var te *transcribe.Error
	switch {
	case errors.Is(err, transcribe.ErrTooLong), errors.Is(err, transcribe.ErrChunkTooLarge), errors.Is(err, ErrAudioTooLarge),
		errors.Is(err, media.ErrLimitExceeded):
		return TranscribeKindLimit
	case errors.Is(err, ErrTranscriptTooLarge), errors.Is(err, transcribe.ErrResponseTooLarge):
		return TranscribeKindTooLarge
	case errors.Is(err, ErrAudioMissing):
		return TranscribeKindMissingAudio
	case errors.Is(err, ErrEmptyTranscript):
		return TranscribeKindEmpty
	case errors.Is(err, media.ErrUnavailable), errors.Is(err, transcribe.ErrInvalidProviderOptions), errors.Is(err, transcribe.ErrInvalidChunking):
		return TranscribeKindUnavailable
	case errors.Is(err, media.ErrMalformed), errors.Is(err, media.ErrNoAudio):
		return TranscribeKindMalformed
	case transcribeRetryable(err):
		return TranscribeKindTransient
	case errors.As(err, &te) && te.StatusCode >= 400 && te.StatusCode < 500:
		return TranscribeKindRefused
	}
	return TranscribeKindFailed
}

func transcribeRetryable(err error) bool {
	return transcribe.IsRetryable(err) || errors.Is(err, errAudioReadShort) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (w *TranscribeAudioWorker) failed(ctx context.Context, logger *slog.Logger, job *river.Job[TranscribeAudioArgs], err error) error {
	kind := TranscribeFailureKind(err)
	err = fmt.Errorf("transcribe recording %s: %s: %w", job.Args.RecordingID, kind, err)
	if transcribeRetryable(err) {
		logger.WarnContext(ctx, "recording not transcribed; River retries it", "error", err, "kind", kind,
			"last_attempt", job.Attempt >= job.MaxAttempts)
		return err
	}
	logger.WarnContext(ctx, "recording not transcribed; cancelled", "error", err, "kind", kind)
	return river.JobCancel(err)
}

func (w *TranscribeAudioWorker) cancel(ctx context.Context, logger *slog.Logger, id uuid.UUID, err error) error {
	kind := TranscribeFailureKind(err)
	err = fmt.Errorf("transcribe recording %s: %s: %w", id, kind, err)
	logger.WarnContext(ctx, "recording not transcribed; cancelled", "error", err, "kind", kind)
	return river.JobCancel(err)
}

func (w *TranscribeAudioWorker) save(ctx context.Context, logger *slog.Logger, in store.TranscriptInput, checkpoint *chunkCheckpoint) error {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), transcribeSaveTimeout)
	defer cancel()
	var created bool
	err := pgx.BeginTxFunc(saveCtx, w.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		t, ok, err := w.store.AddTranscriptTx(saveCtx, tx, in)
		if err != nil || !ok {
			return err
		}
		created = true
		if err := w.settings.FollowUp.TranscriptSaved(saveCtx, tx, t); err != nil {
			return fmt.Errorf("follow up transcript of recording %s: %w", in.RecordingID, err)
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	checkpoint.clear(saveCtx)
	if !created {
		logger.InfoContext(ctx, "transcript already saved by another run; kept it")
		return nil
	}
	logger.InfoContext(ctx, "transcript saved")
	return nil
}

func TranscriptInput(recordingID uuid.UUID, t transcribe.Transcript) (store.TranscriptInput, error) {
	segments := make([]store.TranscriptSegment, 0, len(t.Segments))
	texts := make([]string, 0, len(t.Segments))
	size := 0
	for _, s := range t.Segments {
		text := cutText(oneLine(s.Text), MaxSegmentBytes)
		if text == "" {
			continue
		}
		start := max(s.Start, 0)
		segments = append(segments, store.TranscriptSegment{
			Start:   start,
			End:     max(s.End, start),
			Text:    text,
			Speaker: cutText(oneLine(s.Speaker), MaxSpeakerBytes),
		})
		texts = append(texts, text)
		size += len(text) + 1
		if len(segments) > MaxTranscriptSegments || size > MaxTranscriptBytes {
			return store.TranscriptInput{}, fmt.Errorf("%w: more than %d segments or %d bytes", ErrTranscriptTooLarge, MaxTranscriptSegments, MaxTranscriptBytes)
		}
	}
	text := strings.Join(texts, " ")
	if len(segments) == 0 {
		text = summarize.PlainText(t.Text)
	}
	if len(text) > MaxTranscriptBytes {
		return store.TranscriptInput{}, fmt.Errorf("%w: %d bytes, limit %d", ErrTranscriptTooLarge, len(text), MaxTranscriptBytes)
	}
	if text == "" {
		return store.TranscriptInput{}, fmt.Errorf("%w: %d segments from the model, none with text", ErrEmptyTranscript, len(t.Segments))
	}
	return store.TranscriptInput{RecordingID: recordingID, Text: text, Segments: segments}, nil
}

func oneLine(s string) string {
	return strings.ReplaceAll(summarize.PlainText(s), "\n", " ")
}

func cutText(text string, limit int) string {
	const ellipsis = "…"
	if len(text) <= limit {
		return text
	}
	end := limit - len(ellipsis)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return strings.TrimSpace(text[:end]) + ellipsis
}
