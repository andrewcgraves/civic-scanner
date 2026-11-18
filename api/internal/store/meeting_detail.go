package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type MeetingDetail struct {
	Meeting    Meeting
	Agency     Agency
	Agenda     *AgendaVersion
	Recordings []RecordingDetail
}

type RecordingDetail struct {
	Recording  Recording
	Transcript *Transcript
}

const liveMeetingSQL = `
SELECT m.id, m.agency_id, m.title, m.starts_at, m.status, m.source_url, m.source_id, m.created_at, m.updated_at,
       a.id, a.slug, a.name, a.website, a.created_at
FROM meetings m
JOIN agencies a ON a.id = m.agency_id
WHERE m.id = $1 AND a.status = 'live'`

const latestAgendaWithoutTextSQL = `
SELECT id, meeting_id, version, source_url, '', content_hash, summary, change_note, created_at, updated_at, change_event_pending
FROM agenda_versions
WHERE meeting_id = $1
ORDER BY version DESC
LIMIT 1`

const recordingDetailsSQL = `
SELECT r.id, r.meeting_id, r.source_url, r.audio_key, r.audio_expires_at, r.audio_deleted_at, r.created_at, r.updated_at,
       t.id, t.segments, t.summary, t.created_at, t.updated_at
FROM recordings r
LEFT JOIN transcripts t ON t.recording_id = r.id
WHERE r.meeting_id = $1
ORDER BY r.seq`

func (s *Store) GetLiveMeetingDetail(ctx context.Context, id uuid.UUID) (MeetingDetail, error) {
	var d MeetingDetail
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		d, err = getLiveMeetingDetail(ctx, tx, id)
		return err
	})
	if err != nil {
		return MeetingDetail{}, fmt.Errorf("get meeting detail: %w", err)
	}
	return d, nil
}

func getLiveMeetingDetail(ctx context.Context, q querier, id uuid.UUID) (MeetingDetail, error) {
	var d MeetingDetail
	a := &d.Agency
	m, err := scanMeeting(q.QueryRow(ctx, liveMeetingSQL, id), &a.ID, &a.Slug, &a.Name, &a.Website, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MeetingDetail{}, fmt.Errorf("meeting %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return MeetingDetail{}, fmt.Errorf("get meeting: %w", err)
	}
	d.Meeting = m

	agenda, err := scanAgendaVersion(q.QueryRow(ctx, latestAgendaWithoutTextSQL, id))
	switch {
	case err == nil:
		d.Agenda = &agenda
	case !errors.Is(err, pgx.ErrNoRows):
		return MeetingDetail{}, fmt.Errorf("get latest agenda version: %w", err)
	}

	rows, err := q.Query(ctx, recordingDetailsSQL, id)
	if err != nil {
		return MeetingDetail{}, fmt.Errorf("query recordings: %w", err)
	}
	d.Recordings, err = pgx.CollectRows(rows, scanRecordingDetail)
	if err != nil {
		return MeetingDetail{}, fmt.Errorf("scan recordings: %w", err)
	}
	return d, nil
}

func scanRecordingDetail(row pgx.CollectableRow) (RecordingDetail, error) {
	var (
		tID        *uuid.UUID
		segments   []byte
		summary    *string
		tCreatedAt *time.Time
		tUpdatedAt *time.Time
	)
	r, err := scanRecording(row, &tID, &segments, &summary, &tCreatedAt, &tUpdatedAt)
	if err != nil {
		return RecordingDetail{}, err
	}
	d := RecordingDetail{Recording: r}
	if tID == nil {
		return d, nil
	}
	decoded, err := decodeSegments(segments)
	if err != nil {
		return RecordingDetail{}, err
	}
	d.Transcript = &Transcript{
		ID:          *tID,
		RecordingID: r.ID,
		Segments:    decoded,
		Summary:     summary,
		CreatedAt:   *tCreatedAt,
		UpdatedAt:   *tUpdatedAt,
	}
	return d, nil
}
