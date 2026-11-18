//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func beginTx(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	return tx
}

func agendaPayload(t *testing.T, e store.MeetingEvent) store.AgendaEventPayload {
	t.Helper()
	var p store.AgendaEventPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("payload %s: %v", e.Payload, err)
	}
	return p
}

func TestAddAgendaVersionRecordsPublishedThenChangedEvents(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-events-agenda")

	first, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda one")); err != nil || created {
		t.Fatalf("unchanged add = (created %v, err %v), want not created", created, err)
	}
	second, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda two"))
	if err != nil {
		t.Fatal(err)
	}

	events, err := s.ListMeetingEvents(ctx, meetingID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	for i, want := range []struct {
		eventType store.EventType
		version   store.AgendaVersion
	}{
		{store.EventAgendaPublished, first},
		{store.EventAgendaChanged, second},
	} {
		e := events[i]
		p := agendaPayload(t, e)
		if e.ID == uuid.Nil || e.Type != want.eventType || e.MeetingID != meetingID || e.OccurredAt.IsZero() ||
			p.AgendaVersionID != want.version.ID || p.Version != want.version.Version {
			t.Errorf("events[%d] = %+v (payload %+v), want %s for version %d", i, e, p, want.eventType, want.version.Version)
		}
	}
	if events[0].ID == events[1].ID || events[0].Sequence < 1 || events[1].Sequence <= events[0].Sequence ||
		events[1].OccurredAt.Before(events[0].OccurredAt) {
		t.Errorf("events not distinct and ordered by sequence: %+v", events)
	}
}

func TestAddAgendaVersionTxEventCommitsAndRollsBackWithTheVersion(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-events-agenda-tx")

	rolledBack := beginTx(t, pool)
	if _, _, err := s.AddAgendaVersionTx(ctx, rolledBack, agendaInput(meetingID, "rolled back")); err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if events, err := s.ListMeetingEvents(ctx, meetingID); err != nil || len(events) != 0 {
		t.Fatalf("after rollback = (%+v, %v), want no events", events, err)
	}

	committed := beginTx(t, pool)
	v, _, err := s.AddAgendaVersionTx(ctx, committed, agendaInput(meetingID, "committed"))
	if err != nil {
		t.Fatal(err)
	}
	if events, err := s.ListMeetingEvents(ctx, meetingID); err != nil || len(events) != 0 {
		t.Errorf("uncommitted event visible outside the transaction: (%+v, %v)", events, err)
	}
	if err := committed.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	events, err := s.ListMeetingEvents(ctx, meetingID)
	if err != nil || len(events) != 1 || events[0].Type != store.EventAgendaPublished || agendaPayload(t, events[0]).AgendaVersionID != v.ID {
		t.Errorf("after commit = (%+v, %v), want one agenda_published event for version %s", events, err, v.ID)
	}
}

func TestRecordMeetingEventTxRecordsTranscriptReadyWithItsPayload(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-events-transcript")

	rolledBack := beginTx(t, pool)
	if _, err := s.RecordMeetingEventTx(ctx, rolledBack, store.MeetingEventInput{Type: store.EventTranscriptReady, MeetingID: meetingID}); err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	tx := beginTx(t, pool)
	recorded, err := s.RecordMeetingEventTx(ctx, tx, store.MeetingEventInput{
		Type:      store.EventTranscriptReady,
		MeetingID: meetingID,
		Payload:   map[string]any{"transcript_url": "https://example.com/t.json", "segments": 42},
	})
	if err != nil {
		t.Fatalf("RecordMeetingEventTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	events, err := s.ListMeetingEvents(ctx, meetingID)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListMeetingEvents = (%+v, %v), want only the committed event", events, err)
	}
	e := events[0]
	var payload struct {
		TranscriptURL string `json:"transcript_url"`
		Segments      int    `json:"segments"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if e.ID != recorded.ID || e.Sequence != recorded.Sequence || e.Type != store.EventTranscriptReady || e.MeetingID != meetingID ||
		!e.OccurredAt.Equal(recorded.OccurredAt) || payload.TranscriptURL != "https://example.com/t.json" || payload.Segments != 42 {
		t.Errorf("event = %+v (payload %+v), want the recorded transcript_ready event %+v", e, payload, recorded)
	}
}

func TestRecordMeetingEventTxDefaultsToAnEmptyPayload(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := seedMeeting(t, pool, "store-test-events-empty-payload")

	e, err := store.New(pool).RecordMeetingEventTx(ctx, beginTx(t, pool), store.MeetingEventInput{Type: store.EventTranscriptReady, MeetingID: meetingID})
	if err != nil || string(e.Payload) != "{}" {
		t.Errorf("RecordMeetingEventTx = (payload %s, %v), want {}", e.Payload, err)
	}
}

func TestRecordMeetingEventTxForMissingMeetingIsNotFound(t *testing.T) {
	pool := testdb.Pool(t)

	_, err := store.New(pool).RecordMeetingEventTx(t.Context(), beginTx(t, pool), store.MeetingEventInput{Type: store.EventTranscriptReady, MeetingID: uuid.New()})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRecordMeetingEventTxFailsOnAbortedTransaction(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := seedMeeting(t, pool, "store-test-events-aborted")

	tx := beginTx(t, pool)
	if _, err := tx.Exec(ctx, `SELECT 1/0`); err == nil {
		t.Fatal("division by zero succeeded")
	}
	_, err := store.New(pool).RecordMeetingEventTx(ctx, tx, store.MeetingEventInput{Type: store.EventTranscriptReady, MeetingID: meetingID})
	if err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want an insert error", err)
	}
}

func TestListMeetingEventsOnlyReturnsThatMeeting(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-events-list")
	other := seedMeeting(t, pool, "store-test-events-list-other")

	if _, _, err := s.AddAgendaVersion(ctx, agendaInput(other, "other")); err != nil {
		t.Fatal(err)
	}
	if events, err := s.ListMeetingEvents(ctx, meetingID); err != nil || len(events) != 0 {
		t.Errorf("ListMeetingEvents = (%+v, %v), want empty", events, err)
	}
	if events, err := s.ListMeetingEvents(ctx, uuid.New()); err != nil || len(events) != 0 {
		t.Errorf("unknown meeting = (%+v, %v), want empty", events, err)
	}
}

func TestListMeetingEventsFailsOnClosedPool(t *testing.T) {
	pool, err := store.Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()

	_, err = store.New(pool).ListMeetingEvents(t.Context(), uuid.New())
	if err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want a connection error", err)
	}
}

func failEventInsertsFor(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	suffix := strings.ReplaceAll(meetingID.String(), "-", "")
	function, trigger := "store_test_fail_event_"+suffix, "store_test_fail_event_"+suffix
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.meeting_id = '%s' THEN
        RAISE EXCEPTION 'event insert refused';
    END IF;
    RETURN NEW;
END $$`, function, meetingID)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DROP FUNCTION IF EXISTS `+function+` CASCADE`)
	})
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE INSERT ON meeting_events FOR EACH ROW EXECUTE FUNCTION %s()`, trigger, function)); err != nil {
		t.Fatal(err)
	}
}

func TestAddAgendaVersionRollsBackTheVersionWhenTheEventFails(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-events-fail")
	failEventInsertsFor(t, pool, meetingID)

	_, created, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda"))
	if err == nil || created || !strings.Contains(err.Error(), "record agenda event") || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AddAgendaVersion = (created %v, err %v), want a record agenda event error", created, err)
	}
	if _, err := s.LatestAgendaVersion(ctx, meetingID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("version kept after its event failed: err = %v, want ErrNotFound", err)
	}

	tx := beginTx(t, pool)
	if _, _, err := s.AddAgendaVersionTx(ctx, tx, agendaInput(meetingID, "agenda")); err == nil || !strings.Contains(err.Error(), "record agenda event") {
		t.Errorf("AddAgendaVersionTx err = %v, want a record agenda event error", err)
	}
}
