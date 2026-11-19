//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func deferred(meetingID uuid.UUID, text string) store.AgendaVersionInput {
	in := agendaInput(meetingID, text)
	in.DeferChangeEvent = true
	return in
}

func eventTypes(t *testing.T, s *store.Store, meetingID uuid.UUID) []store.EventType {
	t.Helper()
	events, err := s.ListMeetingEvents(t.Context(), meetingID)
	if err != nil {
		t.Fatal(err)
	}
	var types []store.EventType
	for _, e := range events {
		types = append(types, e.Type)
	}
	return types
}

func TestAnEquivalentTextAddsNoVersion(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-equivalent")

	first, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "1. Call to Order"))
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	in := agendaInput(meetingID, "# 1. Call to Order")
	in.Equivalent = func(latest string) bool {
		seen = append(seen, latest)
		return true
	}
	got, created, err := s.AddAgendaVersion(ctx, in)
	if err != nil || created || got.ID != first.ID {
		t.Errorf("equivalent add = (%v, created %v, %v), want the latest version", got.ID, created, err)
	}
	if !slices.Equal(seen, []string{"1. Call to Order"}) {
		t.Errorf("Equivalent saw %q, want the latest text", seen)
	}

	in = agendaInput(meetingID, "1. Call to Order\n2. Roll Call")
	in.Equivalent = func(string) bool { return false }
	if _, created, err := s.AddAgendaVersion(ctx, in); err != nil || !created {
		t.Errorf("non-equivalent add = (created %v, %v), want created", created, err)
	}
	if !slices.Equal(eventTypes(t, s, meetingID), []store.EventType{store.EventAgendaPublished, store.EventAgendaChanged}) {
		t.Errorf("events = %v", eventTypes(t, s, meetingID))
	}
}

func TestADeferredChangeIsPendingWithoutAnEventUntilRecorded(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-deferred")

	first, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda one"))
	if err != nil || first.ChangeEventPending {
		t.Fatalf("first = (pending %v, %v), want published at once", first.ChangeEventPending, err)
	}
	second, created, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda two"))
	if err != nil || !created || !second.ChangeEventPending || second.Version != 2 {
		t.Fatalf("second = (%+v, created %v, %v), want a pending version 2", second, created, err)
	}
	if !slices.Equal(eventTypes(t, s, meetingID), []store.EventType{store.EventAgendaPublished}) {
		t.Errorf("events before the change is recorded = %v", eventTypes(t, s, meetingID))
	}

	previous, err := s.PreviousAgendaVersion(ctx, second)
	if err != nil || previous.ID != first.ID {
		t.Errorf("previous = (%v, %v), want version 1", previous.ID, err)
	}
	if _, err := s.PreviousAgendaVersion(ctx, first); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("previous of version 1 err = %v, want ErrNotFound", err)
	}

	recorded, ok, err := s.RecordAgendaChange(ctx, second.ID, "Item 2 was replaced.", store.NoteFromModel)
	if err != nil || !ok || recorded.ChangeEventPending || recorded.ChangeNote == nil || *recorded.ChangeNote != "Item 2 was replaced." {
		t.Fatalf("record = (%+v, %v, %v)", recorded, ok, err)
	}
	events, err := s.ListMeetingEvents(ctx, meetingID)
	if err != nil || len(events) != 2 || events[1].Type != store.EventAgendaChanged {
		t.Fatalf("events = (%+v, %v)", events, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["agenda_version_id"] != second.ID.String() || payload["version"] != float64(2) || payload["change_note"] != "Item 2 was replaced." ||
		payload["note_source"] != "model" || payload["found_at"] == nil {
		t.Errorf("payload = %s", events[1].Payload)
	}
	var found agendaFound
	if err := json.Unmarshal(events[1].Payload, &found); err != nil || !found.FoundAt.Equal(second.CreatedAt) || !events[1].OccurredAt.Equal(second.CreatedAt) {
		t.Errorf("found_at = %v, occurred_at = %v, want both the version's created_at %v", found.FoundAt, events[1].OccurredAt, second.CreatedAt)
	}
	if strings.Contains(string(mustEvents(t, s, meetingID)[0].Payload), "change_note") {
		t.Errorf("agenda_published payload carries a change note: %s", mustEvents(t, s, meetingID)[0].Payload)
	}

	again, ok, err := s.RecordAgendaChange(ctx, second.ID, "another note", store.NoteFromDiff)
	if err != nil || ok || again.ChangeNote == nil || *again.ChangeNote != "Item 2 was replaced." {
		t.Errorf("second record = (%v, %v, %v), want nothing written", again.ChangeNote, ok, err)
	}
	if n := len(mustEvents(t, s, meetingID)); n != 2 {
		t.Errorf("events after a second record = %d, want 2", n)
	}
	if _, _, err := s.RecordAgendaChange(ctx, uuid.New(), "note", store.NoteFromDiff); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("record of a missing version err = %v, want ErrNotFound", err)
	}
}

func mustEvents(t *testing.T, s *store.Store, meetingID uuid.UUID) []store.MeetingEvent {
	t.Helper()
	events, err := s.ListMeetingEvents(t.Context(), meetingID)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestRecordingAChangeKeepsANoteAlreadySet(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-keep-note")
	if _, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda one")); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaChangeNote(ctx, second.ID, "Written by hand."); err != nil {
		t.Fatal(err)
	}
	recorded, ok, err := s.RecordAgendaChange(ctx, second.ID, "From the model.", store.NoteFromModel)
	if err != nil || !ok || *recorded.ChangeNote != "Written by hand." {
		t.Errorf("record = (%v, %v, %v), want the existing note kept", recorded.ChangeNote, ok, err)
	}
}

func TestConcurrentRecordsWriteOneEvent(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-record-race")
	if _, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda one")); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda two"))
	if err != nil {
		t.Fatal(err)
	}

	const runs = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		written int
	)
	for range runs {
		wg.Go(func() {
			_, ok, err := s.RecordAgendaChange(ctx, second.ID, "note", store.NoteFromDiff)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				written++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if written != 1 {
		t.Errorf("writes = %d, want 1", written)
	}
	if got := eventTypes(t, s, meetingID); !slices.Equal(got, []store.EventType{store.EventAgendaPublished, store.EventAgendaChanged}) {
		t.Errorf("events = %v, want one agenda_changed", got)
	}
}

func TestListPendingAgendaChangesSkipsRecentAndRecordedVersions(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-pending")
	if _, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda one")); err != nil {
		t.Fatal(err)
	}
	old, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda two"))
	if err != nil {
		t.Fatal(err)
	}
	recent, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda three"))
	if err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `UPDATE agenda_versions SET created_at = $2 WHERE id = $1`, old.ID, cutoff.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	ids, err := pendingIDs(ctx, s, cutoff)
	if err != nil || !slices.Contains(ids, old.ID) || slices.Contains(ids, recent.ID) {
		t.Errorf("pending = (%v, %v), want version 2 and not the recent version 3", ids, err)
	}
	if _, _, err := s.RecordAgendaChange(ctx, old.ID, "note", store.NoteFromDiff); err != nil {
		t.Fatal(err)
	}
	if ids, err := pendingIDs(ctx, s, cutoff); err != nil || slices.Contains(ids, old.ID) {
		t.Errorf("pending after recording = (%v, %v)", ids, err)
	}
}

type agendaFound struct {
	FoundAt time.Time `json:"found_at"`
}

func pendingIDs(ctx context.Context, s *store.Store, before time.Time) ([]uuid.UUID, error) {
	pending, err := s.ListPendingAgendaChanges(ctx, before, 1000)
	var ids []uuid.UUID
	for _, p := range pending {
		ids = append(ids, p.ID)
	}
	return ids, err
}
