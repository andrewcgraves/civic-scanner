//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func seedFeedAgency(t *testing.T, pool *pgxpool.Pool, slug, status string) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, $2, 'https://example.com') RETURNING id`,
		slug, status).Scan(&id); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM meeting_events WHERE meeting_id IN (SELECT id FROM meetings WHERE agency_id = $1)`, id)
		_, _ = pool.Exec(bg, `DELETE FROM agenda_versions WHERE meeting_id IN (SELECT id FROM meetings WHERE agency_id = $1)`, id)
		_, _ = pool.Exec(bg, `DELETE FROM meetings WHERE agency_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM agencies WHERE id = $1`, id)
	})
	return id
}

func seedFeedMeeting(t *testing.T, s *store.Store, agencyID uuid.UUID, sourceID string) store.Meeting {
	t.Helper()
	m, _, err := s.UpsertMeeting(t.Context(), meetingInput(agencyID, sourceID, time.Date(2027, 2, 3, 2, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("UpsertMeeting: %v", err)
	}
	return m
}

func recordTranscriptReady(t *testing.T, s *store.Store, pool *pgxpool.Pool, meetingID uuid.UUID) store.MeetingEvent {
	t.Helper()
	tx := beginTx(t, pool)
	e, err := s.RecordMeetingEventTx(t.Context(), tx, store.MeetingEventInput{Type: store.EventTranscriptReady, MeetingID: meetingID})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGetLiveAgencyFeedReadsEachEventTypeNewestFirst(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedFeedAgency(t, pool, "store-test-feed", "live")
	otherID := seedFeedAgency(t, pool, "store-test-feed-other", "live")
	m := seedFeedMeeting(t, s, agencyID, "feed-board")
	other := seedFeedMeeting(t, s, otherID, "feed-other-board")

	v1, _, err := s.AddAgendaVersion(ctx, agendaInput(m.ID, "agenda one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaSummary(ctx, v1.ID, "First summary."); err != nil {
		t.Fatal(err)
	}
	v2, _, err := s.AddAgendaVersion(ctx, agendaInput(m.ID, "agenda two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaChangeNote(ctx, v2.ID, "Item 2 added."); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddAgendaVersion(ctx, agendaInput(other.ID, "other agenda")); err != nil {
		t.Fatal(err)
	}
	transcript := recordTranscriptReady(t, s, pool, m.ID)

	feed, err := s.GetLiveAgencyFeed(ctx, "store-test-feed")
	if err != nil {
		t.Fatal(err)
	}
	if feed.Agency.ID != agencyID || feed.Agency.Slug != "store-test-feed" || feed.Agency.Website != "https://example.com" {
		t.Errorf("agency = %+v", feed.Agency)
	}
	events, err := s.ListMeetingEvents(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 3 || len(events) != 3 {
		t.Fatalf("items = %+v, events = %+v; want 3 of each", feed.Items, events)
	}
	for i, item := range feed.Items {
		e := events[len(events)-1-i]
		if item.EventID != e.ID || item.Type != e.Type || !item.OccurredAt.Equal(e.OccurredAt) ||
			item.MeetingID != m.ID || item.MeetingTitle != m.Title || !item.MeetingStartsAt.Equal(m.StartsAt) {
			t.Errorf("item %d = %+v, want event %+v", i, item, e)
		}
	}
	if got := feed.Items[0]; got.EventID != transcript.ID || got.AgendaSummary != nil || got.AgendaChange != nil {
		t.Errorf("transcript item = %+v", got)
	}
	if got := feed.Items[1]; got.Type != store.EventAgendaChanged ||
		got.AgendaSummary != nil || got.AgendaChange == nil || *got.AgendaChange != "Item 2 added." {
		t.Errorf("agenda changed item = %+v", got)
	}
	if got := feed.Items[2]; got.Type != store.EventAgendaPublished ||
		got.AgendaSummary == nil || *got.AgendaSummary != "First summary." || got.AgendaChange != nil {
		t.Errorf("agenda published item = %+v", got)
	}
}

func TestGetLiveAgencyFeedNotFoundAndEmpty(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	pending := seedFeedAgency(t, pool, "store-test-feed-pending", "pending")
	seedFeedAgency(t, pool, "store-test-feed-rejected", "rejected")
	seedFeedAgency(t, pool, "store-test-feed-empty", "live")
	m := seedFeedMeeting(t, s, pending, "feed-pending-board")
	if _, _, err := s.AddAgendaVersion(ctx, agendaInput(m.ID, "pending agenda")); err != nil {
		t.Fatal(err)
	}

	for _, slug := range []string{"store-test-feed-unknown", "store-test-feed-pending", "store-test-feed-rejected"} {
		if _, err := s.GetLiveAgencyFeed(ctx, slug); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", slug, err)
		}
	}
	feed, err := s.GetLiveAgencyFeed(ctx, "store-test-feed-empty")
	if err != nil || feed.Agency.Slug != "store-test-feed-empty" || len(feed.Items) != 0 {
		t.Errorf("empty feed = %+v, %v", feed, err)
	}
}

func TestGetLiveAgencyFeedKeepsTheNewestEvents(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedFeedAgency(t, pool, "store-test-feed-limit", "live")
	m := seedFeedMeeting(t, s, agencyID, "feed-limit-board")
	var newest []uuid.UUID
	for range store.MaxFeedItems + 5 {
		newest = append([]uuid.UUID{recordTranscriptReady(t, s, pool, m.ID).ID}, newest...)
	}

	feed, err := s.GetLiveAgencyFeed(ctx, "store-test-feed-limit")
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != store.MaxFeedItems {
		t.Fatalf("items = %d, want %d", len(feed.Items), store.MaxFeedItems)
	}
	for i, item := range feed.Items {
		if item.EventID != newest[i] {
			t.Fatalf("item %d = %s, want %s", i, item.EventID, newest[i])
		}
	}
}

func TestGetLiveAgencyFeedShowsAnEventThatCommitsLate(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedFeedAgency(t, pool, "store-test-feed-late", "live")
	m := seedFeedMeeting(t, s, agencyID, "feed-late-board")

	slow := beginTx(t, pool)
	late, err := s.RecordMeetingEventTx(ctx, slow, store.MeetingEventInput{Type: store.EventTranscriptReady, MeetingID: m.ID})
	if err != nil {
		t.Fatal(err)
	}
	early := recordTranscriptReady(t, s, pool, m.ID)
	if early.Sequence <= late.Sequence {
		t.Fatalf("sequences: early %d, late %d; want the late event to hold the lower one", early.Sequence, late.Sequence)
	}

	before, err := s.GetLiveAgencyFeed(ctx, "store-test-feed-late")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 1 || before.Items[0].EventID != early.ID {
		t.Fatalf("before commit = %+v, want only the committed event", before.Items)
	}
	if err := slow.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	after, err := s.GetLiveAgencyFeed(ctx, "store-test-feed-late")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 2 || after.Items[0].EventID != early.ID || after.Items[1].EventID != late.ID {
		t.Errorf("after commit = %+v, want the late event to appear below the early one", after.Items)
	}
}

func TestGetLiveAgencyFeedToleratesAMalformedAgendaPayload(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedFeedAgency(t, pool, "store-test-feed-payload", "live")
	m := seedFeedMeeting(t, s, agencyID, "feed-payload-board")
	for _, payload := range []string{`{"agenda_version_id": "not-a-uuid"}`, `{"agenda_version_id": 7}`, `{}`, `{"agenda_version_id": "` + uuid.NewString() + `"}`} {
		if _, err := pool.Exec(ctx, `INSERT INTO meeting_events (type, meeting_id, payload) VALUES ('agenda_published', $1, $2)`, m.ID, payload); err != nil {
			t.Fatal(err)
		}
	}

	feed, err := s.GetLiveAgencyFeed(ctx, "store-test-feed-payload")
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 4 {
		t.Fatalf("items = %d, want 4", len(feed.Items))
	}
	for _, item := range feed.Items {
		if item.AgendaSummary != nil || item.AgendaChange != nil {
			t.Errorf("item = %+v, want no agenda version", item)
		}
	}
}

func TestGetLiveAgencyFeedNeverShowsAnotherMeetingsAgenda(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedFeedAgency(t, pool, "store-test-feed-cross", "live")
	m := seedFeedMeeting(t, s, agencyID, "feed-cross-board")
	other := seedFeedMeeting(t, s, agencyID, "feed-cross-other")
	foreign, _, err := s.AddAgendaVersion(ctx, agendaInput(other.ID, "other agenda"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaSummary(ctx, foreign.ID, "Other meeting's summary."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaChangeNote(ctx, foreign.ID, "Other meeting's note."); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO meeting_events (type, meeting_id, payload) VALUES ('agenda_changed', $1, $2)`,
		m.ID, `{"agenda_version_id": "`+foreign.ID.String()+`", "version": 1}`); err != nil {
		t.Fatal(err)
	}

	feed, err := s.GetLiveAgencyFeed(ctx, "store-test-feed-cross")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range feed.Items {
		if item.MeetingID == m.ID && (item.AgendaSummary != nil || item.AgendaChange != nil) {
			t.Errorf("item for %s shows another meeting's agenda: %+v", m.ID, item)
		}
	}
}

func TestGetLiveAgencyFeedQueryError(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.GetLiveAgencyFeed(ctx, "store-test-feed"); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want a query error", err)
	}
}
