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
	Type      EventType
	MeetingID uuid.UUID
	Payload   any
}

type MeetingEvent struct {
	ID         uuid.UUID
	Type       EventType
	MeetingID  uuid.UUID
	Payload    json.RawMessage
	OccurredAt time.Time
}

type AgendaEventPayload struct {
	AgendaVersionID uuid.UUID `json:"agenda_version_id"`
	Version         int       `json:"version"`
}

const meetingEventColumns = `id, type, meeting_id, payload, occurred_at`

func scanMeetingEvent(row pgx.Row) (MeetingEvent, error) {
	var (
		e       MeetingEvent
		payload []byte
	)
	err := row.Scan(&e.ID, &e.Type, &e.MeetingID, &payload, &e.OccurredAt)
	e.Payload = payload
	return e, err
}

func encodeEventPayload(payload any) (string, error) {
	if payload == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
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
	e, err := scanMeetingEvent(q.QueryRow(ctx, `
INSERT INTO meeting_events (type, meeting_id, payload)
VALUES ($1, $2, $3)
RETURNING `+meetingEventColumns,
		string(in.Type), in.MeetingID, payload))
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == foreignKeyViolation {
		return MeetingEvent{}, fmt.Errorf("meeting %s: %w", in.MeetingID, ErrNotFound)
	}
	if err != nil {
		return MeetingEvent{}, fmt.Errorf("insert meeting event: %w", err)
	}
	return e, nil
}

func agendaEventInput(v AgendaVersion) MeetingEventInput {
	eventType := EventAgendaChanged
	if v.Version == 1 {
		eventType = EventAgendaPublished
	}
	return MeetingEventInput{
		Type:      eventType,
		MeetingID: v.MeetingID,
		Payload:   AgendaEventPayload{AgendaVersionID: v.ID, Version: v.Version},
	}
}

func (s *Store) ListMeetingEvents(ctx context.Context, meetingID uuid.UUID) ([]MeetingEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+meetingEventColumns+` FROM meeting_events WHERE meeting_id = $1 ORDER BY occurred_at, id`, meetingID)
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
