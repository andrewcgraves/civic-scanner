package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidAudio   = errors.New("invalid audio")
	ErrAudioChanged   = errors.New("recording audio changed")
	ErrInvalidLimit   = errors.New("invalid limit")
	ErrInvalidFailure = errors.New("invalid download failure")
)

type RecordingInput struct {
	MeetingID uuid.UUID
	SourceURL string
}

type RecordingAudio struct {
	Key       string
	ExpiresAt time.Time
}

type DownloadState struct {
	Attempts   int64
	Failure    string
	FailedAt   *time.Time
	RetryAfter *time.Time
}

type DownloadFailure struct {
	Kind       string
	RetryAfter *time.Time
}

type Recording struct {
	ID             uuid.UUID
	MeetingID      uuid.UUID
	SourceURL      string
	Audio          *RecordingAudio
	AudioDeletedAt *time.Time
	Download       DownloadState
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

var downloadFailureKinds = map[string]bool{
	"unsupported": true, "no_media": true, "no_audio": true, "malformed": true, "refused": true,
	"limit": true, "unavailable": true, "incomplete": true, "transient": true,
}

const recordingColumns = `id, meeting_id, source_url, audio_key, audio_expires_at, audio_deleted_at,
download_attempts, download_failure, download_failed_at, download_retry_after, created_at, updated_at`

func scanRecording(row pgx.Row, extra ...any) (Recording, error) {
	var (
		r         Recording
		key       *string
		expiresAt *time.Time
		failure   *string
	)
	dest := append([]any{&r.ID, &r.MeetingID, &r.SourceURL, &key, &expiresAt, &r.AudioDeletedAt,
		&r.Download.Attempts, &failure, &r.Download.FailedAt, &r.Download.RetryAfter, &r.CreatedAt, &r.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Recording{}, err
	}
	if failure != nil {
		r.Download.Failure = *failure
	}
	if key != nil && expiresAt != nil {
		r.Audio = &RecordingAudio{Key: *key, ExpiresAt: *expiresAt}
	}
	return r, nil
}

func (s *Store) UpsertRecording(ctx context.Context, in RecordingInput) (Recording, bool, error) {
	var (
		r       Recording
		created bool
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, created, err = upsertRecording(ctx, tx, in)
		return err
	})
	if err != nil {
		return Recording{}, false, fmt.Errorf("upsert recording: %w", err)
	}
	return r, created, nil
}

func (s *Store) UpsertRecordingTx(ctx context.Context, tx pgx.Tx, in RecordingInput) (Recording, bool, error) {
	r, created, err := upsertRecording(ctx, tx, in)
	if err != nil {
		return Recording{}, false, fmt.Errorf("upsert recording: %w", err)
	}
	return r, created, nil
}

func upsertRecording(ctx context.Context, q querier, in RecordingInput) (Recording, bool, error) {
	var meetingID uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM meetings WHERE id = $1 FOR KEY SHARE`, in.MeetingID).Scan(&meetingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, false, fmt.Errorf("meeting %s: %w", in.MeetingID, ErrNotFound)
	}
	if err != nil {
		return Recording{}, false, fmt.Errorf("lock meeting: %w", err)
	}

	r, err := scanRecording(q.QueryRow(ctx, `
INSERT INTO recordings (meeting_id, source_url)
VALUES ($1, $2)
ON CONFLICT (meeting_id, source_url) DO NOTHING
RETURNING `+recordingColumns,
		in.MeetingID, in.SourceURL))
	if err == nil {
		return r, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, false, fmt.Errorf("insert recording: %w", err)
	}

	r, err = scanRecording(q.QueryRow(ctx,
		`SELECT `+recordingColumns+` FROM recordings WHERE meeting_id = $1 AND source_url = $2`, in.MeetingID, in.SourceURL))
	if err != nil {
		return Recording{}, false, fmt.Errorf("get existing recording: %w", err)
	}
	return r, false, nil
}

func (s *Store) GetRecording(ctx context.Context, id uuid.UUID) (Recording, error) {
	return getRecording(ctx, s.pool, id)
}

func getRecording(ctx context.Context, q querier, id uuid.UUID) (Recording, error) {
	r, err := scanRecording(q.QueryRow(ctx, `SELECT `+recordingColumns+` FROM recordings WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, ErrNotFound
	}
	if err != nil {
		return Recording{}, fmt.Errorf("get recording: %w", err)
	}
	return r, nil
}

func (s *Store) ListRecordings(ctx context.Context, meetingID uuid.UUID) ([]Recording, error) {
	return queryRecordings(ctx, s.pool, "list recordings",
		`SELECT `+recordingColumns+` FROM recordings WHERE meeting_id = $1 ORDER BY seq`, meetingID)
}

func (s *Store) ListRecordingsWithExpiredAudio(ctx context.Context, now time.Time, limit int) ([]Recording, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list recordings with expired audio: limit %d: %w", limit, ErrInvalidLimit)
	}
	return queryRecordings(ctx, s.pool, "list recordings with expired audio", `
SELECT `+recordingColumns+` FROM recordings
WHERE audio_key IS NOT NULL AND audio_expires_at <= $1
ORDER BY audio_expires_at, id
LIMIT $2`, now, limit)
}

func queryRecordings(ctx context.Context, q querier, op, query string, args ...any) ([]Recording, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	recordings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Recording, error) {
		return scanRecording(row)
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return recordings, nil
}

func validateAudio(audio RecordingAudio) error {
	if audio.Key == "" || audio.ExpiresAt.IsZero() {
		return ErrInvalidAudio
	}
	return nil
}

const setRecordingAudioSQL = `
UPDATE recordings SET audio_key = $2, audio_expires_at = $3, audio_deleted_at = NULL, updated_at = clock_timestamp()
WHERE id = $1
RETURNING ` + recordingColumns

func (s *Store) SetRecordingAudio(ctx context.Context, recordingID uuid.UUID, audio RecordingAudio) (Recording, error) {
	if err := validateAudio(audio); err != nil {
		return Recording{}, err
	}
	return setRecordingAudio(ctx, s.pool, recordingID, audio)
}

func (s *Store) SetRecordingAudioTx(ctx context.Context, tx pgx.Tx, recordingID uuid.UUID, audio RecordingAudio) (Recording, error) {
	if err := validateAudio(audio); err != nil {
		return Recording{}, err
	}
	return setRecordingAudio(ctx, tx, recordingID, audio)
}

func setRecordingAudio(ctx context.Context, q querier, recordingID uuid.UUID, audio RecordingAudio) (Recording, error) {
	r, err := scanRecording(q.QueryRow(ctx, setRecordingAudioSQL, recordingID, audio.Key, audio.ExpiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, fmt.Errorf("set recording audio: recording %s: %w", recordingID, ErrNotFound)
	}
	if err != nil {
		return Recording{}, fmt.Errorf("set recording audio: %w", err)
	}
	return r, nil
}

const addRecordingAudioSQL = `
UPDATE recordings SET audio_key = $2, audio_expires_at = $3,
    download_failure = NULL, download_failed_at = NULL, download_retry_after = NULL, updated_at = clock_timestamp()
WHERE id = $1 AND audio_key IS NULL AND audio_deleted_at IS NULL
RETURNING ` + recordingColumns

func (s *Store) AddRecordingAudio(ctx context.Context, recordingID uuid.UUID, audio RecordingAudio) (Recording, error) {
	if err := validateAudio(audio); err != nil {
		return Recording{}, err
	}
	return addRecordingAudio(ctx, s.pool, recordingID, audio)
}

func (s *Store) AddRecordingAudioTx(ctx context.Context, tx pgx.Tx, recordingID uuid.UUID, audio RecordingAudio) (Recording, error) {
	if err := validateAudio(audio); err != nil {
		return Recording{}, err
	}
	return addRecordingAudio(ctx, tx, recordingID, audio)
}

func addRecordingAudio(ctx context.Context, q querier, recordingID uuid.UUID, audio RecordingAudio) (Recording, error) {
	r, err := scanRecording(q.QueryRow(ctx, addRecordingAudioSQL, recordingID, audio.Key, audio.ExpiresAt))
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, fmt.Errorf("add recording audio: %w", err)
	}
	if _, err := getRecording(ctx, q, recordingID); err != nil {
		return Recording{}, fmt.Errorf("add recording audio: recording %s: %w", recordingID, err)
	}
	return Recording{}, fmt.Errorf("add recording audio: recording %s already has audio or had it deleted: %w", recordingID, ErrAudioChanged)
}

func (s *Store) RecordDownloadFailure(ctx context.Context, recordingID uuid.UUID, failure DownloadFailure) (Recording, error) {
	if !downloadFailureKinds[failure.Kind] {
		return Recording{}, fmt.Errorf("record download failure: kind %q: %w", failure.Kind, ErrInvalidFailure)
	}
	r, err := scanRecording(s.pool.QueryRow(ctx, `
UPDATE recordings SET download_attempts = download_attempts + 1, download_failure = $2,
    download_failed_at = clock_timestamp(), download_retry_after = $3, updated_at = clock_timestamp()
WHERE id = $1
RETURNING `+recordingColumns, recordingID, failure.Kind, failure.RetryAfter))
	if errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, fmt.Errorf("record download failure: recording %s: %w", recordingID, ErrNotFound)
	}
	if err != nil {
		return Recording{}, fmt.Errorf("record download failure: %w", err)
	}
	return r, nil
}

func (s *Store) MarkRecordingAudioDeleted(ctx context.Context, recordingID uuid.UUID, key string) (Recording, error) {
	r, err := scanRecording(s.pool.QueryRow(ctx, `
UPDATE recordings SET audio_key = NULL, audio_expires_at = NULL, audio_deleted_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1 AND audio_key = $2
RETURNING `+recordingColumns, recordingID, key))
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, fmt.Errorf("mark recording audio deleted: %w", err)
	}
	if _, err := getRecording(ctx, s.pool, recordingID); err != nil {
		return Recording{}, fmt.Errorf("mark recording audio deleted: recording %s: %w", recordingID, err)
	}
	return Recording{}, fmt.Errorf("mark recording audio deleted: recording %s no longer has key %q: %w", recordingID, key, ErrAudioChanged)
}
