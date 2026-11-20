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

var (
	ErrZeroContentHash      = errors.New("zero content hash")
	ErrEarlierChangePending = errors.New("an earlier agenda version of the meeting is still pending its change event")
)

type NoteSource string

const (
	NoteFromModel NoteSource = "model"
	NoteFromDiff  NoteSource = "diff"
)

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
	MeetingID        uuid.UUID
	SourceURL        string
	Text             string
	ContentHash      ContentHash
	Equivalent       func(latestText string) bool
	DeferChangeEvent bool
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

	ChangeEventPending bool
	RefreshedInPlace   bool
}

const agendaVersionColumns = `id, meeting_id, version, source_url, extracted_text, content_hash, summary, change_note, created_at, updated_at, change_event_pending`

func scanAgendaVersion(row pgx.Row) (AgendaVersion, error) {
	var (
		v    AgendaVersion
		hash string
	)
	err := row.Scan(&v.ID, &v.MeetingID, &v.Version, &v.SourceURL, &v.Text, &hash, &v.Summary, &v.ChangeNote, &v.CreatedAt, &v.UpdatedAt, &v.ChangeEventPending)
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
	case err == nil && latest.ContentHash == in.ContentHash && latest.SourceURL == in.SourceURL:
		return latest, false, nil
	case err == nil && latest.ContentHash == in.ContentHash:
		return moveAgendaVersion(ctx, q, latest.ID, in.SourceURL)
	case err == nil && in.Equivalent != nil && in.Equivalent(latest.Text):
		return refreshAgendaVersion(ctx, q, latest.ID, in)
	case err != nil && !errors.Is(err, ErrNotFound):
		return AgendaVersion{}, false, err
	}

	number := latest.Version + 1
	pending := false
	if number > 1 {
		pending = in.DeferChangeEvent
		if !pending {
			if err := q.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM agenda_versions WHERE meeting_id = $1 AND change_event_pending)`,
				in.MeetingID).Scan(&pending); err != nil {
				return AgendaVersion{}, false, fmt.Errorf("check pending agenda changes: %w", err)
			}
		}
	}
	v, err := scanAgendaVersion(q.QueryRow(ctx, `
INSERT INTO agenda_versions (meeting_id, version, source_url, extracted_text, content_hash, change_event_pending)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING `+agendaVersionColumns,
		in.MeetingID, number, in.SourceURL, in.Text, in.ContentHash.String(), pending))
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("insert agenda version: %w", err)
	}
	if pending {
		return v, true, nil
	}
	if _, err := recordMeetingEvent(ctx, q, agendaEventInput(v)); err != nil {
		return AgendaVersion{}, false, fmt.Errorf("record agenda event: %w", err)
	}
	return v, true, nil
}

func refreshAgendaVersion(ctx context.Context, q querier, versionID uuid.UUID, in AgendaVersionInput) (AgendaVersion, bool, error) {
	v, err := scanAgendaVersion(q.QueryRow(ctx, `
UPDATE agenda_versions
SET extracted_text = $2, content_hash = $3, source_url = $4, updated_at = clock_timestamp()
WHERE id = $1
RETURNING `+agendaVersionColumns, versionID, in.Text, in.ContentHash.String(), in.SourceURL))
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("refresh agenda version: %w", err)
	}
	v.RefreshedInPlace = true
	return v, false, nil
}

func moveAgendaVersion(ctx context.Context, q querier, versionID uuid.UUID, sourceURL string) (AgendaVersion, bool, error) {
	v, err := scanAgendaVersion(q.QueryRow(ctx, `
UPDATE agenda_versions
SET source_url = $2, updated_at = clock_timestamp()
WHERE id = $1
RETURNING `+agendaVersionColumns, versionID, sourceURL))
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("move agenda version: %w", err)
	}
	return v, false, nil
}

func (s *Store) LatestAgendaSourceURLsTx(ctx context.Context, tx pgx.Tx, agencyID uuid.UUID, sourceIDs []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `
SELECT m.source_id, v.source_url
FROM meetings m
JOIN LATERAL (
    SELECT source_url FROM agenda_versions WHERE meeting_id = m.id ORDER BY version DESC LIMIT 1
) v ON true
WHERE m.agency_id = $1 AND m.source_id = ANY($2)`, agencyID, sourceIDs)
	if err != nil {
		return nil, fmt.Errorf("list latest agenda source urls: %w", err)
	}
	urls := map[string]string{}
	var sourceID, sourceURL string
	if _, err := pgx.ForEachRow(rows, []any{&sourceID, &sourceURL}, func() error {
		urls[sourceID] = sourceURL
		return nil
	}); err != nil {
		return nil, fmt.Errorf("scan latest agenda source urls: %w", err)
	}
	return urls, nil
}

func (s *Store) EarlierPendingAgendaVersions(ctx context.Context, v AgendaVersion) ([]AgendaVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+agendaVersionColumns+` FROM agenda_versions WHERE meeting_id = $1 AND version < $2 AND change_event_pending ORDER BY version`,
		v.MeetingID, v.Version)
	if err != nil {
		return nil, fmt.Errorf("list earlier pending agenda versions: %w", err)
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AgendaVersion, error) {
		return scanAgendaVersion(row)
	})
	if err != nil {
		return nil, fmt.Errorf("scan earlier pending agenda versions: %w", err)
	}
	return versions, nil
}

func (s *Store) AgendaJobFailedBefore(ctx context.Context, kind string, versionID uuid.UUID) (bool, error) {
	var failed bool
	err := s.pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM river_job
    WHERE kind = $1 AND args->>'agenda_version_id' = $2 AND state IN ('cancelled', 'discarded')
)`, kind, versionID.String()).Scan(&failed)
	if err != nil {
		return false, fmt.Errorf("check failed agenda jobs: %w", err)
	}
	return failed, nil
}

func (s *Store) PreviousAgendaVersion(ctx context.Context, v AgendaVersion) (AgendaVersion, error) {
	prev, err := scanAgendaVersion(s.pool.QueryRow(ctx,
		`SELECT `+agendaVersionColumns+` FROM agenda_versions WHERE meeting_id = $1 AND version < $2 ORDER BY version DESC LIMIT 1`,
		v.MeetingID, v.Version))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgendaVersion{}, fmt.Errorf("version before %d of meeting %s: %w", v.Version, v.MeetingID, ErrNotFound)
	}
	if err != nil {
		return AgendaVersion{}, fmt.Errorf("get previous agenda version: %w", err)
	}
	return prev, nil
}

func (s *Store) RecordAgendaChange(ctx context.Context, versionID uuid.UUID, note string, source NoteSource) (AgendaVersion, bool, error) {
	var (
		v        AgendaVersion
		recorded bool
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var earlier bool
		if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM agenda_versions e JOIN agenda_versions v ON v.meeting_id = e.meeting_id
    WHERE v.id = $1 AND e.version < v.version AND e.change_event_pending
)`, versionID).Scan(&earlier); err != nil {
			return fmt.Errorf("check earlier pending versions: %w", err)
		}
		if earlier {
			return ErrEarlierChangePending
		}
		var err error
		v, err = scanAgendaVersion(tx.QueryRow(ctx, `
UPDATE agenda_versions
SET change_note = COALESCE(change_note, $2), change_event_pending = false, updated_at = clock_timestamp()
WHERE id = $1 AND change_event_pending
RETURNING `+agendaVersionColumns, versionID, note))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("set change note: %w", err)
		}
		recorded = true
		if v.ChangeNote == nil || *v.ChangeNote != note {
			source = ""
		}
		_, err = recordMeetingEvent(ctx, tx, agendaChangeEventInput(v, source))
		return err
	})
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("record agenda change: %w", err)
	}
	if !recorded {
		v, err = s.GetAgendaVersion(ctx, versionID)
		if err != nil {
			return AgendaVersion{}, false, fmt.Errorf("record agenda change: %w", err)
		}
	}
	return v, recorded, nil
}

type PendingAgendaChange struct {
	ID        uuid.UUID
	CreatedAt time.Time
}

func (s *Store) ListPendingAgendaChanges(ctx context.Context, createdBefore time.Time, limit int) ([]PendingAgendaChange, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, created_at FROM agenda_versions WHERE change_event_pending AND created_at < $1 ORDER BY created_at, id LIMIT $2`, createdBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending agenda changes: %w", err)
	}
	pending, err := pgx.CollectRows(rows, pgx.RowToStructByPos[PendingAgendaChange])
	if err != nil {
		return nil, fmt.Errorf("scan pending agenda changes: %w", err)
	}
	return pending, nil
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

func (s *Store) GetAgendaVersion(ctx context.Context, versionID uuid.UUID) (AgendaVersion, error) {
	v, err := scanAgendaVersion(s.pool.QueryRow(ctx,
		`SELECT `+agendaVersionColumns+` FROM agenda_versions WHERE id = $1`, versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgendaVersion{}, fmt.Errorf("agenda version %s: %w", versionID, ErrNotFound)
	}
	if err != nil {
		return AgendaVersion{}, fmt.Errorf("get agenda version: %w", err)
	}
	return v, nil
}

func (s *Store) SetAgendaSummaryIfUnset(ctx context.Context, versionID uuid.UUID, summary string) (AgendaVersion, bool, error) {
	v, err := scanAgendaVersion(s.pool.QueryRow(ctx, setAgendaSummaryIfUnsetSQL, versionID, summary))
	if err == nil {
		return v, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AgendaVersion{}, false, fmt.Errorf("set agenda summary if unset: %w", err)
	}
	existing, err := s.GetAgendaVersion(ctx, versionID)
	if err != nil {
		return AgendaVersion{}, false, fmt.Errorf("set agenda summary if unset: %w", err)
	}
	return existing, false, nil
}

const (
	setAgendaSummaryIfUnsetSQL = `UPDATE agenda_versions SET summary = $2, updated_at = clock_timestamp() WHERE id = $1 AND summary IS NULL RETURNING ` + agendaVersionColumns
	setAgendaSummarySQL        = `UPDATE agenda_versions SET summary = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING ` + agendaVersionColumns
	setAgendaChangeNoteSQL     = `UPDATE agenda_versions SET change_note = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING ` + agendaVersionColumns
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

func (s *Store) SummaryOfSameContent(ctx context.Context, v AgendaVersion) (string, bool, error) {
	var summary string
	err := s.pool.QueryRow(ctx, `
SELECT summary FROM agenda_versions
WHERE meeting_id = $1 AND content_hash = $2 AND id <> $3 AND summary IS NOT NULL
ORDER BY version DESC LIMIT 1`, v.MeetingID, v.ContentHash.String(), v.ID).Scan(&summary)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("summary of same content: %w", err)
	}
	return summary, true, nil
}

type UnsummarizedParams struct {
	From         time.Time
	To           time.Time
	FailedKind   string
	FailedWithin time.Duration
	Limit        int
}

func (s *Store) ListUnsummarizedAgendaVersions(ctx context.Context, p UnsummarizedParams) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT v.id
FROM meetings m
JOIN agencies a ON a.id = m.agency_id AND a.status = 'live'
JOIN LATERAL (
    SELECT id, summary FROM agenda_versions WHERE meeting_id = m.id ORDER BY version DESC LIMIT 1
) v ON true
WHERE m.starts_at >= $1 AND m.starts_at <= $2
  AND v.summary IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM river_job j
    WHERE j.kind = $3
      AND j.args->>'agenda_version_id' = v.id::text
      AND j.state IN ('cancelled', 'discarded')
      AND j.finalized_at >= now() - $4::interval
  )
ORDER BY m.starts_at DESC, m.id
LIMIT $5`, p.From, p.To, p.FailedKind, p.FailedWithin, p.Limit)
	if err != nil {
		return nil, fmt.Errorf("list unsummarized agenda versions: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("scan unsummarized agenda versions: %w", err)
	}
	return ids, nil
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
