package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	DefaultMeetingsLimit = 50
	MaxMeetingsLimit     = 200
)

var (
	ErrNotFound      = errors.New("not found")
	ErrInvalidCursor = errors.New("invalid cursor")
)

type Meeting struct {
	ID        uuid.UUID
	AgencyID  uuid.UUID
	Title     string
	StartsAt  time.Time
	Status    string
	SourceURL string
	SourceID  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type MeetingInput struct {
	AgencyID  uuid.UUID
	Title     string
	StartsAt  time.Time
	Status    string
	SourceURL string
	SourceID  string
}

type ListMeetingsParams struct {
	Cursor string
	Limit  int
}

type MeetingPage struct {
	Meetings   []Meeting
	NextCursor string
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const meetingColumns = `id, agency_id, title, starts_at, status, source_url, source_id, created_at, updated_at`

func scanMeeting(row pgx.Row, extra ...any) (Meeting, error) {
	var m Meeting
	dest := append([]any{&m.ID, &m.AgencyID, &m.Title, &m.StartsAt, &m.Status, &m.SourceURL, &m.SourceID, &m.CreatedAt, &m.UpdatedAt}, extra...)
	err := row.Scan(dest...)
	return m, err
}

func (s *Store) UpsertMeeting(ctx context.Context, in MeetingInput) (Meeting, bool, error) {
	return upsertMeeting(ctx, s.pool, in)
}

func (s *Store) UpsertMeetingTx(ctx context.Context, tx pgx.Tx, in MeetingInput) (Meeting, bool, error) {
	return upsertMeeting(ctx, tx, in)
}

func upsertMeeting(ctx context.Context, q querier, in MeetingInput) (Meeting, bool, error) {
	var created bool
	m, err := scanMeeting(q.QueryRow(ctx, `
INSERT INTO meetings (agency_id, title, starts_at, status, source_url, source_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (agency_id, source_id) DO UPDATE SET
    title = EXCLUDED.title,
    starts_at = EXCLUDED.starts_at,
    status = CASE WHEN meetings.status = 'held' THEN meetings.status ELSE EXCLUDED.status END,
    source_url = EXCLUDED.source_url,
    updated_at = clock_timestamp()
RETURNING `+meetingColumns+`, (xmax = 0)`,
		in.AgencyID, in.Title, in.StartsAt, in.Status, in.SourceURL, in.SourceID), &created)
	if err != nil {
		return Meeting{}, false, fmt.Errorf("upsert meeting: %w", err)
	}
	return m, created, nil
}

func (s *Store) GetMeeting(ctx context.Context, id uuid.UUID) (Meeting, error) {
	m, err := scanMeeting(s.pool.QueryRow(ctx, `SELECT `+meetingColumns+` FROM meetings WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Meeting{}, ErrNotFound
	}
	if err != nil {
		return Meeting{}, fmt.Errorf("get meeting: %w", err)
	}
	return m, nil
}

type meetingCursor struct {
	AgencyID uuid.UUID `json:"a"`
	StartsAt time.Time `json:"s"`
	ID       uuid.UUID `json:"i"`
}

const meetingCursorChecksumSize = 8

func meetingCursorChecksum(payload []byte) []byte {
	sum := sha256.Sum256(payload)
	return sum[:meetingCursorChecksumSize]
}

func encodeMeetingCursor(m Meeting) string {
	payload, _ := json.Marshal(meetingCursor{AgencyID: m.AgencyID, StartsAt: m.StartsAt.UTC(), ID: m.ID})
	return base64.RawURLEncoding.EncodeToString(append(payload, meetingCursorChecksum(payload)...))
}

func decodeMeetingCursor(cursor string, agencyID uuid.UUID) (meetingCursor, error) {
	var c meetingCursor
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) <= meetingCursorChecksumSize {
		return c, ErrInvalidCursor
	}
	payload, checksum := raw[:len(raw)-meetingCursorChecksumSize], raw[len(raw)-meetingCursorChecksumSize:]
	if !bytes.Equal(checksum, meetingCursorChecksum(payload)) {
		return c, ErrInvalidCursor
	}
	if err := json.Unmarshal(payload, &c); err != nil || c.StartsAt.IsZero() || c.ID == uuid.Nil || c.AgencyID != agencyID {
		return meetingCursor{}, ErrInvalidCursor
	}
	return c, nil
}

func clampMeetingsLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultMeetingsLimit
	case limit > MaxMeetingsLimit:
		return MaxMeetingsLimit
	default:
		return limit
	}
}

func (s *Store) ListMeetings(ctx context.Context, agencyID uuid.UUID, params ListMeetingsParams) (MeetingPage, error) {
	limit := clampMeetingsLimit(params.Limit)

	query := `SELECT ` + meetingColumns + ` FROM meetings WHERE agency_id = $1`
	args := []any{agencyID}
	if params.Cursor != "" {
		c, err := decodeMeetingCursor(params.Cursor, agencyID)
		if err != nil {
			return MeetingPage{}, err
		}
		query += ` AND (starts_at, id) < ($2, $3)`
		args = append(args, c.StartsAt, c.ID)
	}
	query += fmt.Sprintf(` ORDER BY starts_at DESC, id DESC LIMIT %d`, limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return MeetingPage{}, fmt.Errorf("query meetings: %w", err)
	}
	meetings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Meeting, error) {
		return scanMeeting(row)
	})
	if err != nil {
		return MeetingPage{}, fmt.Errorf("scan meetings: %w", err)
	}

	page := MeetingPage{Meetings: meetings}
	if len(meetings) > limit {
		page.Meetings = meetings[:limit]
		page.NextCursor = encodeMeetingCursor(page.Meetings[limit-1])
	}
	return page, nil
}
