package webhooks

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	SampleMeetingTitle    = "Sample meeting for a test webhook"
	SampleChangeNote      = "This is a test webhook; no agenda changed."
	sampleMeetingLeadDays = 7
)

func SampleEvent(eventType store.EventType, agencySlug, agencyName string, now time.Time) (Event, error) {
	now = now.UTC()
	e := Event{
		ID:         uuid.New(),
		Type:       eventType,
		OccurredAt: now.Truncate(time.Second),
		AgencySlug: agencySlug,
		AgencyName: agencyName,
		Meeting: store.Meeting{
			ID:       uuid.New(),
			Title:    SampleMeetingTitle,
			StartsAt: now.AddDate(0, 0, sampleMeetingLeadDays).Truncate(time.Hour),
		},
		Test: true,
	}
	var payload any
	switch eventType {
	case store.EventAgendaPublished:
		payload = store.AgendaEventPayload{AgendaVersionID: uuid.New(), Version: 1}
	case store.EventAgendaChanged:
		note := SampleChangeNote
		payload = store.AgendaEventPayload{AgendaVersionID: uuid.New(), Version: 2, ChangeNote: &note, NoteSource: store.NoteFromDiff}
	case store.EventTranscriptReady:
		payload = store.TranscriptEventPayload{TranscriptID: uuid.New(), RecordingID: uuid.New()}
	default:
		return Event{}, fmt.Errorf("%w: type %q", ErrUnsupportedEvent, eventType)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode sample event: %w", err)
	}
	e.Payload = raw
	return e, nil
}
