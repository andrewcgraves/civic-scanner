package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type EventType string

const (
	EventAgendaPublished EventType = "agenda_published"
	EventAgendaChanged   EventType = "agenda_changed"
	EventTranscriptReady EventType = "transcript_ready"
)

const foreignKeyViolation = "23503"

var (
	ErrUnknownEventType = errors.New("unknown event type")
	ErrInvalidPayload   = errors.New("event payload is not a JSON object")
)

func (t EventType) Valid() bool {
	switch t {
	case EventAgendaPublished, EventAgendaChanged, EventTranscriptReady:
		return true
	default:
		return false
	}
}

type MeetingEventInput struct {
	Type       EventType
	MeetingID  uuid.UUID
	Payload    any
	OccurredAt time.Time
}

type MeetingEvent struct {
	ID         uuid.UUID
	Sequence   int64
	Type       EventType
	MeetingID  uuid.UUID
	Payload    json.RawMessage
	OccurredAt time.Time
}

type AgendaEventPayload struct {
	AgendaVersionID uuid.UUID  `json:"agenda_version_id"`
	Version         int        `json:"version"`
	FoundAt         *time.Time `json:"found_at,omitempty"`
	ChangeNote      *string    `json:"change_note,omitempty"`
	NoteSource      NoteSource `json:"note_source,omitempty"`
}

const meetingEventColumns = `id, sequence, type, meeting_id, payload, occurred_at`

func scanMeetingEvent(row pgx.Row) (MeetingEvent, error) {
	var (
		e       MeetingEvent
		payload []byte
	)
	err := row.Scan(&e.ID, &e.Sequence, &e.Type, &e.MeetingID, &payload, &e.OccurredAt)
	e.Payload = payload
	return e, err
}

func encodeEventPayload(payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	if string(raw) == "null" {
		return "{}", nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", ErrInvalidPayload
	}
	return string(raw), nil
}

func (s *Store) RecordMeetingEventTx(ctx context.Context, tx pgx.Tx, in MeetingEventInput) (MeetingEvent, error) {
	e, err := recordMeetingEvent(ctx, tx, in)
	if err != nil {
		return MeetingEvent{}, fmt.Errorf("record meeting event: %w", err)
	}
	return e, nil
}

func recordMeetingEvent(ctx context.Context, q querier, in MeetingEventInput) (MeetingEvent, error) {
	if !in.Type.Valid() {
		return MeetingEvent{}, fmt.Errorf("%w: %q", ErrUnknownEventType, in.Type)
	}
	payload, err := encodeEventPayload(in.Payload)
	if err != nil {
		return MeetingEvent{}, err
	}
	var occurredAt *time.Time
	if !in.OccurredAt.IsZero() {
		occurredAt = &in.OccurredAt
	}
	e, err := scanMeetingEvent(q.QueryRow(ctx, `
INSERT INTO meeting_events (type, meeting_id, payload, occurred_at)
VALUES ($1, $2, $3, COALESCE($4, clock_timestamp()))
RETURNING `+meetingEventColumns,
		string(in.Type), in.MeetingID, payload, occurredAt))
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == foreignKeyViolation {
		return MeetingEvent{}, fmt.Errorf("meeting %s: %w", in.MeetingID, ErrNotFound)
	}
	if err != nil {
		return MeetingEvent{}, fmt.Errorf("insert meeting event: %w", err)
	}
	return e, nil
}

func agendaEventInput(v AgendaVersion) MeetingEventInput {
	if v.Version == 1 {
		return MeetingEventInput{
			Type:      EventAgendaPublished,
			MeetingID: v.MeetingID,
			Payload:   AgendaEventPayload{AgendaVersionID: v.ID, Version: v.Version},
		}
	}
	return agendaChangeEventInput(v, "")
}

func agendaChangeEventInput(v AgendaVersion, source NoteSource) MeetingEventInput {
	foundAt := v.CreatedAt
	return MeetingEventInput{
		Type:       EventAgendaChanged,
		MeetingID:  v.MeetingID,
		Payload:    AgendaEventPayload{AgendaVersionID: v.ID, Version: v.Version, FoundAt: &foundAt, ChangeNote: v.ChangeNote, NoteSource: source},
		OccurredAt: v.CreatedAt,
	}
}

func (s *Store) ListMeetingEvents(ctx context.Context, meetingID uuid.UUID) ([]MeetingEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+meetingEventColumns+` FROM meeting_events WHERE meeting_id = $1 ORDER BY sequence`, meetingID)
	if err != nil {
		return nil, fmt.Errorf("query meeting events: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (MeetingEvent, error) {
		return scanMeetingEvent(row)
	})
	if err != nil {
		return nil, fmt.Errorf("scan meeting events: %w", err)
	}
	return events, nil
}
