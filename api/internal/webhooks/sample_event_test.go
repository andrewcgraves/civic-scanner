package webhooks_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

func TestSampleEventIsAMarkedTestOfEachType(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 30, 15, 500, time.FixedZone("PST", -8*3600))
	for _, eventType := range []store.EventType{store.EventAgendaPublished, store.EventAgendaChanged, store.EventTranscriptReady} {
		e, err := webhooks.SampleEvent(eventType, "la-metro", "LA Metro", now)
		if err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
		body, err := webhooks.Payload(store.WebhookPayloadV1, e)
		if err != nil {
			t.Fatalf("%s: payload: %v", eventType, err)
		}
		var got v1.WebhookEventV1
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Test == nil || !*got.Test {
			t.Errorf("%s: not marked as a test: %s", eventType, body)
		}
		if got.Id != e.ID || got.Id == uuid.Nil || string(got.Type) != string(eventType) || got.PayloadVersion != v1.WebhookPayloadV1 {
			t.Errorf("%s: event = %+v", eventType, got)
		}
		if got.Agency != (v1.WebhookAgency{Slug: "la-metro", Name: "LA Metro"}) {
			t.Errorf("%s: agency = %+v", eventType, got.Agency)
		}
		if got.Meeting.Title != webhooks.SampleMeetingTitle || got.Meeting.Id == uuid.Nil || got.Meeting.SourceUrl != nil ||
			!got.Meeting.StartsAt.Equal(time.Date(2026, 11, 27, 17, 0, 0, 0, time.UTC)) {
			t.Errorf("%s: meeting = %+v", eventType, got.Meeting)
		}
		if !got.OccurredAt.Equal(time.Date(2026, 11, 20, 17, 30, 15, 0, time.UTC)) {
			t.Errorf("%s: occurredAt = %s", eventType, got.OccurredAt)
		}
		switch eventType {
		case store.EventAgendaPublished:
			if got.Agenda == nil || got.Agenda.Version != 1 || got.Agenda.VersionId == uuid.Nil || got.Agenda.ChangeNote != nil || got.Transcript != nil {
				t.Errorf("published: agenda %+v, transcript %+v", got.Agenda, got.Transcript)
			}
		case store.EventAgendaChanged:
			if got.Agenda == nil || got.Agenda.Version != 2 || got.Agenda.ChangeNote == nil || *got.Agenda.ChangeNote != webhooks.SampleChangeNote ||
				got.Agenda.ChangeNoteSource == nil || *got.Agenda.ChangeNoteSource != v1.WebhookChangeNoteFromDiff {
				t.Errorf("changed: agenda %+v", got.Agenda)
			}
		case store.EventTranscriptReady:
			if got.Transcript == nil || got.Transcript.Id == uuid.Nil || got.Transcript.RecordingId == uuid.Nil || got.Agenda != nil {
				t.Errorf("transcript: agenda %+v, transcript %+v", got.Agenda, got.Transcript)
			}
		}
	}
}

func TestSampleEventsHaveFreshIDs(t *testing.T) {
	now := time.Now()
	a, errA := webhooks.SampleEvent(store.EventTranscriptReady, "x", "X", now)
	b, errB := webhooks.SampleEvent(store.EventTranscriptReady, "x", "X", now)
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a.ID == b.ID || a.Meeting.ID == b.Meeting.ID || string(a.Payload) == string(b.Payload) {
		t.Errorf("two samples share ids: %+v %+v", a, b)
	}
}

func TestSampleEventRefusesAnUnknownType(t *testing.T) {
	if _, err := webhooks.SampleEvent("meeting_cancelled", "x", "X", time.Now()); !errors.Is(err, webhooks.ErrUnsupportedEvent) {
		t.Errorf("err = %v", err)
	}
}

func TestRealPayloadsAreNotMarkedAsTests(t *testing.T) {
	e, err := webhooks.SampleEvent(store.EventAgendaPublished, "x", "X", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Test = false
	body, err := webhooks.Payload(store.WebhookPayloadV1, e)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["test"]; ok {
		t.Errorf("a real payload carries test: %s", body)
	}
}

func TestSampleEventsAreMarkedInThePresets(t *testing.T) {
	e, err := webhooks.SampleEvent(store.EventAgendaChanged, "la-metro", "LA Metro", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	discord, err := webhooks.Body(store.WebhookFormatDiscord, store.WebhookPayloadV1, "", e)
	if err != nil {
		t.Fatal(err)
	}
	var d webhooks.DiscordMessage
	if err := json.Unmarshal(discord, &d); err != nil || len(d.Embeds) != 1 || !strings.HasPrefix(d.Embeds[0].Description, "**Test: Agenda changed**") || d.Embeds[0].URL != "" {
		t.Errorf("discord = %s, %v", discord, err)
	}
	slack, err := webhooks.Body(store.WebhookFormatSlack, store.WebhookPayloadV1, "", e)
	if err != nil {
		t.Fatal(err)
	}
	var s webhooks.SlackMessage
	if err := json.Unmarshal(slack, &s); err != nil || !strings.HasPrefix(s.Text, "Test: Agenda changed: "+webhooks.SampleMeetingTitle) ||
		len(s.Blocks) == 0 || !strings.HasPrefix(s.Blocks[0].Text.Text, "Test: Agenda changed: ") {
		t.Errorf("slack = %s, %v", slack, err)
	}
	e.Test = false
	plain, err := webhooks.Body(store.WebhookFormatSlack, store.WebhookPayloadV1, "", e)
	if err != nil || strings.Contains(string(plain), "Test: ") {
		t.Errorf("a real slack message is marked: %s, %v", plain, err)
	}
}
