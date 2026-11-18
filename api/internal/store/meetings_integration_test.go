//go:build integration

package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func seedAgency(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&id); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM meetings WHERE agency_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM agencies WHERE id = $1`, id)
	})
	return id
}

func meetingInput(agencyID uuid.UUID, sourceID string, startsAt time.Time) store.MeetingInput {
	return store.MeetingInput{
		AgencyID:  agencyID,
		Title:     "Board of Directors " + sourceID,
		StartsAt:  startsAt,
		Status:    "scheduled",
		SourceURL: "https://example.com/meetings/" + sourceID,
		SourceID:  sourceID,
	}
}

func TestUpsertMeetingCreatesThenUpdatesInPlace(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-upsert")
	start := time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC)

	created, wasCreated, err := s.UpsertMeeting(ctx, meetingInput(agencyID, "m-1", start))
	if err != nil {
		t.Fatalf("UpsertMeeting: %v", err)
	}
	if !wasCreated {
		t.Error("first upsert: created = false, want true")
	}
	if created.ID == uuid.Nil || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Errorf("unexpected meeting: %+v", created)
	}

	changed := meetingInput(agencyID, "m-1", start.Add(24*time.Hour))
	changed.Title = "Rescheduled Board Meeting"
	changed.Status = "held"
	changed.SourceURL = "https://example.com/changed"
	updated, wasCreated, err := s.UpsertMeeting(ctx, changed)
	if err != nil {
		t.Fatalf("second UpsertMeeting: %v", err)
	}
	if wasCreated {
		t.Error("second upsert: created = true, want false")
	}
	if updated.ID != created.ID || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("identity changed: %+v -> %+v", created, updated)
	}
	if updated.Title != changed.Title || !updated.StartsAt.Equal(changed.StartsAt) ||
		updated.Status != "held" || updated.SourceURL != changed.SourceURL {
		t.Errorf("fields not updated: %+v", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want after %v", updated.UpdatedAt, created.UpdatedAt)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM meetings WHERE agency_id = $1`, agencyID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("meeting rows = %d, want 1", count)
	}
}

func TestUpsertMeetingSameSourceIDForDifferentAgenciesCreatesBoth(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	a := seedAgency(t, pool, "store-test-upsert-a")
	b := seedAgency(t, pool, "store-test-upsert-b")
	start := time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC)

	for _, agencyID := range []uuid.UUID{a, b} {
		_, wasCreated, err := s.UpsertMeeting(ctx, meetingInput(agencyID, "shared", start))
		if err != nil || !wasCreated {
			t.Fatalf("UpsertMeeting = (created %v, err %v), want created", wasCreated, err)
		}
	}
}

func TestUpsertMeetingRejectsInvalidInput(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-upsert-invalid")

	bad := meetingInput(agencyID, "bad", time.Now())
	bad.Status = "bogus"
	if _, _, err := s.UpsertMeeting(ctx, bad); err == nil {
		t.Error("invalid status was accepted")
	}
	if _, _, err := s.UpsertMeeting(ctx, meetingInput(uuid.New(), "orphan", time.Now())); err == nil {
		t.Error("unknown agency was accepted")
	}
}

func TestUpsertMeetingTxCommitsAndRollsBackWithCaller(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-upsert-tx")
	start := time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC)

	rolledBack, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMeetingTx(ctx, rolledBack, meetingInput(agencyID, "tx-rollback", start)); err != nil {
		t.Fatalf("UpsertMeetingTx: %v", err)
	}
	if err := rolledBack.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	committed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	meeting, wasCreated, err := s.UpsertMeetingTx(ctx, committed, meetingInput(agencyID, "tx-commit", start))
	if err != nil || !wasCreated {
		t.Fatalf("UpsertMeetingTx = (created %v, err %v), want created", wasCreated, err)
	}
	if _, err := s.GetMeeting(ctx, meeting.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("uncommitted meeting visible outside the transaction: err = %v", err)
	}
	if err := committed.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM meetings WHERE agency_id = $1`, agencyID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("meeting rows = %d, want 1 (rolled back one must not persist)", count)
	}
}

func TestGetMeeting(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-get")
	start := time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC)

	created, _, err := s.UpsertMeeting(ctx, meetingInput(agencyID, "get-1", start))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMeeting(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetMeeting: %v", err)
	}
	if got.ID != created.ID || got.AgencyID != agencyID || got.SourceID != "get-1" || !got.StartsAt.Equal(start) {
		t.Errorf("GetMeeting = %+v", got)
	}

	if _, err := s.GetMeeting(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing meeting: err = %v, want ErrNotFound", err)
	}
}

func TestListMeetingsPagesNewestFirstStableUnderTies(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-list")
	other := seedAgency(t, pool, "store-test-list-other")
	base := time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)

	want := map[string]bool{}
	for i := range 7 {
		start := base.Add(time.Duration(i/2) * 24 * time.Hour)
		m, _, err := s.UpsertMeeting(ctx, meetingInput(agencyID, fmt.Sprintf("list-%d", i), start))
		if err != nil {
			t.Fatal(err)
		}
		want[m.ID.String()] = true
	}
	if _, _, err := s.UpsertMeeting(ctx, meetingInput(other, "list-other", base)); err != nil {
		t.Fatal(err)
	}

	var got []store.Meeting
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		page, err := s.ListMeetings(ctx, agencyID, store.ListMeetingsParams{Cursor: cursor, Limit: 3})
		if err != nil {
			t.Fatalf("ListMeetings: %v", err)
		}
		if len(page.Meetings) > 3 {
			t.Fatalf("page has %d meetings, limit 3", len(page.Meetings))
		}
		got = append(got, page.Meetings...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if len(got) != 7 {
		t.Fatalf("listed %d meetings, want 7", len(got))
	}
	seen := map[string]bool{}
	for i, m := range got {
		if m.AgencyID != agencyID || !want[m.ID.String()] || seen[m.ID.String()] {
			t.Errorf("unexpected or duplicate meeting at %d: %+v", i, m)
		}
		seen[m.ID.String()] = true
		if i > 0 && m.StartsAt.After(got[i-1].StartsAt) {
			t.Errorf("meeting %d starts after meeting %d", i, i-1)
		}
	}
}

func TestListMeetingsLastFullPageHasNoCursor(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-list-exact")
	base := time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)
	for i := range 2 {
		if _, _, err := s.UpsertMeeting(ctx, meetingInput(agencyID, fmt.Sprintf("exact-%d", i), base.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.ListMeetings(ctx, agencyID, store.ListMeetingsParams{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Meetings) != 2 || page.NextCursor != "" {
		t.Errorf("page = %d meetings, cursor %q; want 2 and no cursor", len(page.Meetings), page.NextCursor)
	}
}

func TestListMeetingsDefaultsAndClampsLimit(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-list-limit")
	base := time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)
	for i := range store.MaxMeetingsLimit + 1 {
		if _, err := pool.Exec(ctx,
			`INSERT INTO meetings (agency_id, title, starts_at, source_id) VALUES ($1, 't', $2, $3)`,
			agencyID, base.Add(time.Duration(i)*time.Minute), fmt.Sprintf("limit-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{"zero uses default", 0, store.DefaultMeetingsLimit},
		{"negative uses default", -5, store.DefaultMeetingsLimit},
		{"above maximum is clamped", 100000, store.MaxMeetingsLimit},
	} {
		page, err := s.ListMeetings(ctx, agencyID, store.ListMeetingsParams{Limit: tc.limit})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(page.Meetings) != tc.want || page.NextCursor == "" {
			t.Errorf("%s: got %d meetings (cursor %q), want %d with a cursor", tc.name, len(page.Meetings), page.NextCursor, tc.want)
		}
	}
}

func TestListMeetingsRejectsMalformedCursor(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-list-cursor")

	_, err := s.ListMeetings(ctx, agencyID, store.ListMeetingsParams{Cursor: "not-a-cursor"})
	if !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("err = %v, want ErrInvalidCursor", err)
	}
}

func TestListMeetingsEmptyForUnknownAgency(t *testing.T) {
	ctx := t.Context()
	s := store.New(testdb.Pool(t))

	page, err := s.ListMeetings(ctx, uuid.New(), store.ListMeetingsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Meetings) != 0 || page.NextCursor != "" {
		t.Errorf("page = %+v, want empty", page)
	}
}
