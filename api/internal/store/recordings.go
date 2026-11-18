package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrInvalidAudio = errors.New("invalid audio")

const foreignKeyViolation = "23503"

type RecordingInput struct {
	MeetingID uuid.UUID
	SourceURL string
}

type RecordingAudio struct {
	Key       string
	ExpiresAt time.Time
}

type Recording struct {
	ID        uuid.UUID
	MeetingID uuid.UUID
	SourceURL string
	Audio     *RecordingAudio
	CreatedAt time.Time
	UpdatedAt time.Time
}

const recordingColumns = `id, meeting_id, source_url, audio_key, audio_expires_at, created_at, updated_at`

func scanRecording(row pgx.Row, extra ...any) (Recording, error) {
	var (
		r         Recording
		key       *string
		expiresAt *time.Time
	)
	dest := append([]any{&r.ID, &r.MeetingID, &r.SourceURL, &key, &expiresAt, &r.CreatedAt, &r.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Recording{}, err
	}
	if key != nil && expiresAt != nil {
		r.Audio = &RecordingAudio{Key: *key, ExpiresAt: *expiresAt}
	}
	return r, nil
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation
}

func (s *Store) UpsertRecording(ctx context.Context, in RecordingInput) (Recording, bool, error) {
	return upsertRecording(ctx, s.pool, in)
}

func (s *Store) UpsertRecordingTx(ctx context.Context, tx pgx.Tx, in RecordingInput) (Recording, bool, error) {
	return upsertRecording(ctx, tx, in)
}

func upsertRecording(ctx context.Context, q querier, in RecordingInput) (Recording, bool, error) {
	var created bool
	r, err := scanRecording(q.QueryRow(ctx, `
INSERT INTO recordings (meeting_id, source_url)
VALUES ($1, $2)
ON CONFLICT (meeting_id, source_url) DO UPDATE SET source_url = EXCLUDED.source_url
RETURNING `+recordingColumns+`, (xmax = 0)`,
		in.MeetingID, in.SourceURL), &created)
	if isForeignKeyViolation(err) {
		return Recording{}, false, fmt.Errorf("upsert recording: meeting %s: %w", in.MeetingID, ErrNotFound)
	}
	if err != nil {
		return Recording{}, false, fmt.Errorf("upsert recording: %w", err)
	}
	return r, created, nil
}

func (s *Store) GetRecording(ctx context.Context, id uuid.UUID) (Recording, error) {
	r, err := scanRecording(s.pool.QueryRow(ctx, `SELECT `+recordingColumns+` FROM recordings WHERE id = $1`, id))
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
		`SELECT `+recordingColumns+` FROM recordings WHERE meeting_id = $1 ORDER BY created_at, id`, meetingID)
}

func (s *Store) ListRecordingsWithExpiredAudio(ctx context.Context, now time.Time, limit int) ([]Recording, error) {
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

const (
	setRecordingAudioSQL   = `UPDATE recordings SET audio_key = $2, audio_expires_at = $3, updated_at = clock_timestamp() WHERE id = $1 RETURNING ` + recordingColumns
	clearRecordingAudioSQL = `UPDATE recordings SET audio_key = NULL, audio_expires_at = NULL, updated_at = clock_timestamp() WHERE id = $1 RETURNING ` + recordingColumns
)

func (s *Store) SetRecordingAudio(ctx context.Context, recordingID uuid.UUID, audio RecordingAudio) (Recording, error) {
	if err := validateAudio(audio); err != nil {
		return Recording{}, err
	}
	return updateRecording(ctx, s.pool, "set recording audio", setRecordingAudioSQL, recordingID, audio.Key, audio.ExpiresAt)
}

func (s *Store) SetRecordingAudioTx(ctx context.Context, tx pgx.Tx, recordingID uuid.UUID, audio RecordingAudio) (Recording, error) {
	if err := validateAudio(audio); err != nil {
		return Recording{}, err
	}
	return updateRecording(ctx, tx, "set recording audio", setRecordingAudioSQL, recordingID, audio.Key, audio.ExpiresAt)
}

func (s *Store) ClearRecordingAudio(ctx context.Context, recordingID uuid.UUID) (Recording, error) {
	return updateRecording(ctx, s.pool, "clear recording audio", clearRecordingAudioSQL, recordingID)
}

func updateRecording(ctx context.Context, q querier, op, query string, recordingID uuid.UUID, args ...any) (Recording, error) {
	r, err := scanRecording(q.QueryRow(ctx, query, append([]any{recordingID}, args...)...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Recording{}, fmt.Errorf("%s: recording %s: %w", op, recordingID, ErrNotFound)
	}
	if err != nil {
		return Recording{}, fmt.Errorf("%s: %w", op, err)
	}
	return r, nil
}
