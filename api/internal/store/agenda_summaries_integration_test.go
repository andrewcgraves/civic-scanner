//go:build integration

package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func seedMeetingAt(t *testing.T, pool *pgxpool.Pool, agencyID uuid.UUID, sourceID string, startsAt time.Time) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	m, _, err := store.New(pool).UpsertMeeting(ctx, meetingInput(agencyID, sourceID, startsAt))
	if err != nil {
		t.Fatalf("UpsertMeeting: %v", err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM river_job WHERE kind = 'summarize_agenda_store_test'`)
		_, _ = pool.Exec(bg, `DELETE FROM meeting_events WHERE meeting_id = $1`, m.ID)
		_, _ = pool.Exec(bg, `DELETE FROM agenda_versions WHERE meeting_id = $1`, m.ID)
	})
	return m.ID
}

func addVersion(t *testing.T, s *store.Store, meetingID uuid.UUID, text string) store.AgendaVersion {
	t.Helper()
	v, created, err := s.AddAgendaVersion(t.Context(), agendaInput(meetingID, text))
	if err != nil || !created {
		t.Fatalf("AddAgendaVersion(%q) = (created %v, err %v)", text, created, err)
	}
	return v
}

func TestSummaryOfSameContentFindsAnEarlierSummaryOfTheSameText(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-same-content")
	other := seedMeeting(t, pool, "store-test-same-content-other")
	v1 := addVersion(t, s, meetingID, "agenda A")
	if _, err := s.SetAgendaSummary(ctx, v1.ID, "Summary of A."); err != nil {
		t.Fatal(err)
	}
	otherV := addVersion(t, s, other, "agenda B")
	if _, err := s.SetAgendaSummary(ctx, otherV.ID, "Summary of B elsewhere."); err != nil {
		t.Fatal(err)
	}
	v2 := addVersion(t, s, meetingID, "agenda B")
	v3 := addVersion(t, s, meetingID, "agenda A")

	if got, ok, err := s.SummaryOfSameContent(ctx, v3); err != nil || !ok || got != "Summary of A." {
		t.Errorf("flip back = (%q, %v, %v), want version 1's summary", got, ok, err)
	}
	if got, ok, err := s.SummaryOfSameContent(ctx, v2); err != nil || ok {
		t.Errorf("new text = (%q, %v, %v), want none: another meeting's summary must not be used", got, ok, err)
	}
	if _, ok, err := s.SummaryOfSameContent(ctx, v1); err != nil || ok {
		t.Errorf("itself = (%v, %v), want none", ok, err)
	}
}

func TestListUnsummarizedAgendaVersionsPicksLatestUnsummarizedInWindow(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	base := time.Date(2093, 4, 1, 17, 0, 0, 0, time.UTC)
	live := seedAgency(t, pool, "store-test-sweep-live")
	if _, err := pool.Exec(ctx, `UPDATE agencies SET status = 'live', website = 'https://sweep.example.com' WHERE id = $1`, live); err != nil {
		t.Fatal(err)
	}
	pending := seedAgency(t, pool, "store-test-sweep-pending")

	soon := seedMeetingAt(t, pool, live, "soon", base.AddDate(0, 0, 3))
	later := seedMeetingAt(t, pool, live, "later", base.AddDate(0, 0, 10))
	done := seedMeetingAt(t, pool, live, "done", base.AddDate(0, 0, 4))
	outside := seedMeetingAt(t, pool, live, "outside", base.AddDate(0, 0, 200))
	hidden := seedMeetingAt(t, pool, pending, "hidden", base.AddDate(0, 0, 5))
	failed := seedMeetingAt(t, pool, live, "failed", base.AddDate(0, 0, 6))
	failedLongAgo := seedMeetingAt(t, pool, live, "failed-long-ago", base.AddDate(0, 0, 7))

	addVersion(t, s, soon, "soon v1")
	soonLatest := addVersion(t, s, soon, "soon v2")
	laterV := addVersion(t, s, later, "later v1")
	doneV := addVersion(t, s, done, "done v1")
	if _, err := s.SetAgendaSummary(ctx, doneV.ID, "Done."); err != nil {
		t.Fatal(err)
	}
	addVersion(t, s, outside, "outside v1")
	addVersion(t, s, hidden, "hidden v1")
	failedV := addVersion(t, s, failed, "failed v1")
	failedLongAgoV := addVersion(t, s, failedLongAgo, "failed long ago v1")
	recent := time.Now().Add(-time.Hour)
	for _, j := range []struct {
		id    uuid.UUID
		state string
		at    time.Time
	}{{failedV.ID, "discarded", recent}, {failedLongAgoV.ID, "cancelled", time.Now().AddDate(0, 0, -8)}, {laterV.ID, "completed", recent}} {
		if _, err := pool.Exec(ctx, `
INSERT INTO river_job (kind, args, state, finalized_at, max_attempts, queue, attempt, attempted_at)
VALUES ('summarize_agenda_store_test', jsonb_build_object('agenda_version_id', $1::text), $2, $3, 5, 'store_test', 1, $3)`,
			j.id, j.state, j.at); err != nil {
			t.Fatalf("insert river job: %v", err)
		}
	}

	params := store.UnsummarizedParams{
		From:         base,
		To:           base.AddDate(0, 0, 90),
		FailedKind:   "summarize_agenda_store_test",
		FailedWithin: 7 * 24 * time.Hour,
		Limit:        10,
	}
	got, err := s.ListUnsummarizedAgendaVersions(ctx, params)
	if err != nil {
		t.Fatalf("ListUnsummarizedAgendaVersions: %v", err)
	}
	want := []uuid.UUID{laterV.ID, failedLongAgoV.ID, soonLatest.ID}
	if !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v (latest first by meeting start, no summarized, outside, hidden or recently failed)", got, want)
	}

	params.Limit = 1
	if got, err := s.ListUnsummarizedAgendaVersions(ctx, params); err != nil || !slices.Equal(got, want[:1]) {
		t.Errorf("limit 1 = (%v, %v), want %v", got, err, want[:1])
	}
}

func TestAgendaSummaryReadsFailOnClosedPool(t *testing.T) {
	ctx := t.Context()
	pool, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	s := store.New(pool)

	if _, _, err := s.SummaryOfSameContent(ctx, store.AgendaVersion{ID: uuid.New(), ContentHash: store.HashContent("a")}); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("SummaryOfSameContent err = %v, want a connection error", err)
	}
	if _, err := s.ListUnsummarizedAgendaVersions(ctx, store.UnsummarizedParams{Limit: 1}); err == nil {
		t.Error("ListUnsummarizedAgendaVersions on a closed pool returned no error")
	}
}
