package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestEventTypeValid(t *testing.T) {
	for _, valid := range []EventType{EventAgendaPublished, EventAgendaChanged, EventTranscriptReady} {
		if !valid.Valid() {
			t.Errorf("%q is not valid", valid)
		}
	}
	for _, invalid := range []EventType{"", "agenda_deleted", "AGENDA_PUBLISHED"} {
		if invalid.Valid() {
			t.Errorf("%q is valid", invalid)
		}
	}
}

func TestEncodeEventPayloadAcceptsOnlyObjects(t *testing.T) {
	for name, tc := range map[string]struct {
		payload any
		want    string
	}{
		"nil":        {nil, "{}"},
		"empty map":  {map[string]any{}, "{}"},
		"map":        {map[string]any{"transcript_url": "https://example.com/t.json"}, `{"transcript_url":"https://example.com/t.json"}`},
		"struct":     {AgendaEventPayload{Version: 2}, `{"agenda_version_id":"00000000-0000-0000-0000-000000000000","version":2}`},
		"raw object": {json.RawMessage(`{"a":1}`), `{"a":1}`},
	} {
		got, err := encodeEventPayload(tc.payload)
		if err != nil || got != tc.want {
			t.Errorf("%s: encodeEventPayload = (%q, %v), want %q", name, got, err, tc.want)
		}
	}

	for name, payload := range map[string]any{
		"string":   "text",
		"number":   7,
		"array":    []string{"a"},
		"null map": map[string]any(nil),
		"raw null": json.RawMessage(`null`),
		"func":     func() {},
	} {
		if _, err := encodeEventPayload(payload); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s: err = %v, want ErrInvalidPayload", name, err)
		}
	}
}

func TestRecordMeetingEventTxRejectsBadInputBeforeTouchingTheDatabase(t *testing.T) {
	for name, tc := range map[string]struct {
		in   MeetingEventInput
		want error
	}{
		"unknown type": {MeetingEventInput{Type: "agenda_deleted", MeetingID: uuid.New()}, ErrUnknownEventType},
		"empty type":   {MeetingEventInput{MeetingID: uuid.New()}, ErrUnknownEventType},
		"bad payload":  {MeetingEventInput{Type: EventTranscriptReady, MeetingID: uuid.New(), Payload: []int{1}}, ErrInvalidPayload},
	} {
		if _, err := New(nil).RecordMeetingEventTx(t.Context(), nil, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestAgendaEventInputPublishesFirstVersionAndChangesLater(t *testing.T) {
	meetingID := uuid.New()
	for version, want := range map[int]EventType{1: EventAgendaPublished, 2: EventAgendaChanged, 9: EventAgendaChanged} {
		v := AgendaVersion{ID: uuid.New(), MeetingID: meetingID, Version: version}
		got := agendaEventInput(v)
		payload, ok := got.Payload.(AgendaEventPayload)
		if got.Type != want || got.MeetingID != meetingID || !ok || payload.AgendaVersionID != v.ID || payload.Version != version {
			t.Errorf("version %d: agendaEventInput = %+v, want type %q with the version in the payload", version, got, want)
		}
	}
}
