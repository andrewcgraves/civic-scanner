package webhooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var (
	ErrUnsupportedVersion = errors.New("unsupported webhook payload version")
	ErrUnsupportedEvent   = errors.New("unsupported meeting event")
	ErrUnsupportedFormat  = errors.New("unsupported webhook format")
)

type Event struct {
	ID         uuid.UUID
	Type       store.EventType
	Payload    json.RawMessage
	OccurredAt time.Time
	AgencySlug string
	AgencyName string
	Meeting    store.Meeting
	PageURL    string
}

func Body(format store.WebhookFormat, version string, e Event) ([]byte, error) {
	switch format {
	case store.WebhookFormatStandard, "":
		return Payload(version, e)
	case store.WebhookFormatDiscord:
		return discordBody(e)
	case store.WebhookFormatSlack:
		return slackBody(e)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, format)
	}
}

func Payload(version string, e Event) ([]byte, error) {
	if version != store.WebhookPayloadV1 {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedVersion, version)
	}
	body, err := eventV1(e)
	if err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func eventV1(e Event) (v1.WebhookEventV1, error) {
	body := v1.WebhookEventV1{
		Id:             e.ID,
		Type:           v1.WebhookEventType(e.Type),
		PayloadVersion: v1.WebhookPayloadV1,
		OccurredAt:     e.OccurredAt.UTC(),
		Agency:         v1.WebhookAgency{Slug: e.AgencySlug, Name: e.AgencyName},
		Meeting: v1.WebhookMeeting{
			Id:       e.Meeting.ID,
			Title:    e.Meeting.Title,
			StartsAt: e.Meeting.StartsAt.UTC(),
		},
	}
	if e.Meeting.SourceURL != "" {
		body.Meeting.SourceUrl = &e.Meeting.SourceURL
	}
	switch e.Type {
	case store.EventAgendaPublished, store.EventAgendaChanged:
		var p store.AgendaEventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.AgendaVersionID == uuid.Nil || p.Version < 1 {
			return v1.WebhookEventV1{}, fmt.Errorf("%w: %s payload of event %s", ErrUnsupportedEvent, e.Type, e.ID)
		}
		agenda := v1.WebhookAgenda{VersionId: p.AgendaVersionID, Version: p.Version}
		if e.Type == store.EventAgendaChanged && p.ChangeNote != nil && *p.ChangeNote != "" {
			agenda.ChangeNote = p.ChangeNote
			if source := v1.WebhookAgendaChangeNoteSource(p.NoteSource); source.Valid() {
				agenda.ChangeNoteSource = &source
			}
		}
		body.Agenda = &agenda
	case store.EventTranscriptReady:
		var p store.TranscriptEventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.TranscriptID == uuid.Nil || p.RecordingID == uuid.Nil {
			return v1.WebhookEventV1{}, fmt.Errorf("%w: %s payload of event %s", ErrUnsupportedEvent, e.Type, e.ID)
		}
		body.Transcript = &v1.WebhookTranscript{Id: p.TranscriptID, RecordingId: p.RecordingID}
	default:
		return v1.WebhookEventV1{}, fmt.Errorf("%w: type %q", ErrUnsupportedEvent, e.Type)
	}
	return body, nil
}
