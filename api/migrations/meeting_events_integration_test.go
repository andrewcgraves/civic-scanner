//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func insertEventMeeting(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	agencyID := insertMeetingAgency(t, pool, slug)
	ctx := t.Context()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO meetings (agency_id, title, starts_at, source_id) VALUES ($1, 't', now(), $2) RETURNING id`,
		agencyID, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM meeting_events WHERE meeting_id = $1`, id)
	})
	return id
}

func insertMeetingEvent(ctx context.Context, pool *pgxpool.Pool, meetingID, eventType, payload string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO meeting_events (type, meeting_id, payload) VALUES ($1, $2, $3::jsonb)`, eventType, meetingID, payload)
	return err
}

func TestMeetingEventsDefaults(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertEventMeeting(t, pool, "migration-test-event-defaults")

	var (
		idSet, occurredSet bool
		payload            string
	)
	if err := pool.QueryRow(ctx, `
INSERT INTO meeting_events (type, meeting_id) VALUES ('transcript_ready', $1)
RETURNING id IS NOT NULL, payload::text, occurred_at IS NOT NULL`, meetingID).Scan(&idSet, &payload, &occurredSet); err != nil {
		t.Fatal(err)
	}
	if !idSet || payload != "{}" || !occurredSet {
		t.Errorf("id set = %v, payload = %q, occurred_at set = %v; want true, {}, true", idSet, payload, occurredSet)
	}
}

func TestMeetingEventsAcceptEachType(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertEventMeeting(t, pool, "migration-test-event-types")

	for _, eventType := range []string{"agenda_published", "agenda_changed", "transcript_ready"} {
		if err := insertMeetingEvent(ctx, pool, meetingID, eventType, `{"a":1}`); err != nil {
			t.Errorf("%s: %v", eventType, err)
		}
	}
}

func TestMeetingEventsRejectInvalidRows(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertEventMeeting(t, pool, "migration-test-event-invalid")

	for name, tc := range map[string]struct{ meetingID, eventType, payload string }{
		"unknown type":       {meetingID, "agenda_deleted", `{}`},
		"array payload":      {meetingID, "agenda_published", `[]`},
		"string payload":     {meetingID, "agenda_published", `"x"`},
		"null json payload":  {meetingID, "agenda_published", `null`},
		"missing meeting id": {"00000000-0000-0000-0000-000000000001", "agenda_published", `{}`},
	} {
		if err := insertMeetingEvent(ctx, pool, tc.meetingID, tc.eventType, tc.payload); err == nil {
			t.Errorf("%s: row was accepted", name)
		}
	}
}

func TestMeetingEventsKeepTheirMeetingFromBeingDeleted(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertEventMeeting(t, pool, "migration-test-event-restrict")

	if err := insertMeetingEvent(ctx, pool, meetingID, "transcript_ready", `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM meetings WHERE id = $1`, meetingID); err == nil {
		t.Error("meeting with events was deleted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM meeting_events WHERE meeting_id = $1`, meetingID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("%d events after the refused delete, want 1", count)
	}
}

func TestMeetingEventsSequenceIsAssignedAndIncreasing(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertEventMeeting(t, pool, "migration-test-event-sequence")

	var first, second int64
	for _, dest := range []*int64{&first, &second} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO meeting_events (type, meeting_id) VALUES ('transcript_ready', $1) RETURNING sequence`, meetingID).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if first < 1 || second <= first {
		t.Errorf("sequences = %d, %d; want positive and increasing", first, second)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO meeting_events (sequence, type, meeting_id) VALUES (1, 'transcript_ready', $1)`, meetingID); err == nil {
		t.Error("an explicit sequence was accepted")
	}
}
