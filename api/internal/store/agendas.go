package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrZeroContentHash = errors.New("zero content hash")

type ContentHash struct {
	hex string
}

func HashContent(content string) ContentHash {
	sum := sha256.Sum256([]byte(content))
	return ContentHash{hex: hex.EncodeToString(sum[:])}
}

func (h ContentHash) String() string {
	return h.hex
}

func (h ContentHash) IsZero() bool {
	return h.hex == ""
}

type AgendaVersionInput struct {
	MeetingID   uuid.UUID
	SourceURL   string
	Text        string
	ContentHash ContentHash
}

type AgendaVersion struct {
	ID          uuid.UUID
	MeetingID   uuid.UUID
	Version     int
	SourceURL   string
	Text        string
	ContentHash ContentHash
	Summary     *string
	ChangeNote  *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const agendaVersionColumns = `id, meeting_id, version, source_url, extracted_text, content_hash, summary, change_note, created_at, updated_at`

func scanAgendaVersion(row pgx.Row) (AgendaVersion, error) {
	var (
		v    AgendaVersion
		hash string
	)
	err := row.Scan(&v.ID, &v.MeetingID, &v.Version, &v.SourceURL, &v.Text, &hash, &v.Summary, &v.ChangeNote, &v.CreatedAt, &v.UpdatedAt)
	v.ContentHash = ContentHash{hex: hash}
	return v, err
}

func (s *Store) AddAgendaVersion(ctx context.Context, in AgendaVersionInput) (AgendaVersion, bool, error) {
	if in.ContentHash.IsZero() {
		return AgendaVersion{}, false, ErrZeroContentHash
	}
	var (
		v       AgendaVersion
		created bool
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		v, created, err = addAgendaVersion(ctx, tx, in)
		return err
	})
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("add agenda version: %w", err)
	}
	return v, created, nil
}

func (s *Store) AddAgendaVersionTx(ctx context.Context, tx pgx.Tx, in AgendaVersionInput) (AgendaVersion, bool, error) {
	if in.ContentHash.IsZero() {
		return AgendaVersion{}, false, ErrZeroContentHash
	}
	v, created, err := addAgendaVersion(ctx, tx, in)
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("add agenda version: %w", err)
	}
	return v, created, nil
}

func addAgendaVersion(ctx context.Context, q querier, in AgendaVersionInput) (AgendaVersion, bool, error) {
	var meetingID uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM meetings WHERE id = $1 FOR NO KEY UPDATE`, in.MeetingID).Scan(&meetingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgendaVersion{}, false, fmt.Errorf("meeting %s: %w", in.MeetingID, ErrNotFound)
	}
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("lock meeting: %w", err)
	}

	latest, err := latestAgendaVersion(ctx, q, in.MeetingID)
	switch {
	case err == nil && latest.ContentHash == in.ContentHash:
		return latest, false, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return AgendaVersion{}, false, err
	}

	v, err := scanAgendaVersion(q.QueryRow(ctx, `
INSERT INTO agenda_versions (meeting_id, version, source_url, extracted_text, content_hash)
VALUES ($1, $2, $3, $4, $5)
RETURNING `+agendaVersionColumns,
		in.MeetingID, latest.Version+1, in.SourceURL, in.Text, in.ContentHash.String()))
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("insert agenda version: %w", err)
	}
	return v, true, nil
}

func (s *Store) LatestAgendaVersion(ctx context.Context, meetingID uuid.UUID) (AgendaVersion, error) {
	return latestAgendaVersion(ctx, s.pool, meetingID)
}

func latestAgendaVersion(ctx context.Context, q querier, meetingID uuid.UUID) (AgendaVersion, error) {
	v, err := scanAgendaVersion(q.QueryRow(ctx,
		`SELECT `+agendaVersionColumns+` FROM agenda_versions WHERE meeting_id = $1 ORDER BY version DESC LIMIT 1`, meetingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgendaVersion{}, ErrNotFound
	}
	if err != nil {
		return AgendaVersion{}, fmt.Errorf("get latest agenda version: %w", err)
	}
	return v, nil
}

const (
	setAgendaSummarySQL    = `UPDATE agenda_versions SET summary = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING ` + agendaVersionColumns
	setAgendaChangeNoteSQL = `UPDATE agenda_versions SET change_note = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING ` + agendaVersionColumns
)

func (s *Store) SetAgendaSummary(ctx context.Context, versionID uuid.UUID, summary string) (AgendaVersion, error) {
	return updateAgendaVersion(ctx, s.pool, "set agenda summary", setAgendaSummarySQL, versionID, summary)
}

func (s *Store) SetAgendaSummaryTx(ctx context.Context, tx pgx.Tx, versionID uuid.UUID, summary string) (AgendaVersion, error) {
	return updateAgendaVersion(ctx, tx, "set agenda summary", setAgendaSummarySQL, versionID, summary)
}

func (s *Store) SetAgendaChangeNote(ctx context.Context, versionID uuid.UUID, note string) (AgendaVersion, error) {
	return updateAgendaVersion(ctx, s.pool, "set agenda change note", setAgendaChangeNoteSQL, versionID, note)
}

func (s *Store) SetAgendaChangeNoteTx(ctx context.Context, tx pgx.Tx, versionID uuid.UUID, note string) (AgendaVersion, error) {
	return updateAgendaVersion(ctx, tx, "set agenda change note", setAgendaChangeNoteSQL, versionID, note)
}

func updateAgendaVersion(ctx context.Context, q querier, op, query string, versionID uuid.UUID, value string) (AgendaVersion, error) {
	v, err := scanAgendaVersion(q.QueryRow(ctx, query, versionID, value))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgendaVersion{}, fmt.Errorf("%s: agenda version %s: %w", op, versionID, ErrNotFound)
	}
	if err != nil {
		return AgendaVersion{}, fmt.Errorf("%s: %w", op, err)
	}
	return v, nil
}

func (s *Store) ListAgendaVersions(ctx context.Context, meetingID uuid.UUID) ([]AgendaVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+agendaVersionColumns+` FROM agenda_versions WHERE meeting_id = $1 ORDER BY version DESC`, meetingID)
	if err != nil {
		return nil, fmt.Errorf("query agenda versions: %w", err)
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AgendaVersion, error) {
		return scanAgendaVersion(row)
	})
	if err != nil {
		return nil, fmt.Errorf("scan agenda versions: %w", err)
	}
	return versions, nil
}
