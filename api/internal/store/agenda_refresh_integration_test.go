//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func equivalentInput(meetingID uuid.UUID, text, sourceURL string) store.AgendaVersionInput {
	in := deferred(meetingID, text)
	in.SourceURL = sourceURL
	in.Equivalent = func(string) bool { return true }
	return in
}

func TestAnEquivalentRenderingRefreshesTheLatestVersionInPlace(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-refresh")

	first, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "1. Call to Order"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaSummary(ctx, first.ID, "The board meets."); err != nil {
		t.Fatal(err)
	}

	moved := "https://agency.test/moved/agenda.pdf"
	refreshed, created, err := s.AddAgendaVersion(ctx, equivalentInput(meetingID, "# 1. Call to Order", moved))
	if err != nil || created || !refreshed.RefreshedInPlace {
		t.Fatalf("refresh = (created %v, refreshed %v, %v)", created, refreshed.RefreshedInPlace, err)
	}
	if refreshed.ID != first.ID || refreshed.Version != 1 || !refreshed.CreatedAt.Equal(first.CreatedAt) ||
		refreshed.Text != "# 1. Call to Order" || refreshed.ContentHash != store.HashContent("# 1. Call to Order") ||
		refreshed.SourceURL != moved || refreshed.Summary == nil || *refreshed.Summary != "The board meets." {
		t.Errorf("refreshed = %+v", refreshed)
	}

	again, created, err := s.AddAgendaVersion(ctx, equivalentInput(meetingID, "# 1. Call to Order", moved))
	if err != nil || created || again.RefreshedInPlace || !again.UpdatedAt.Equal(refreshed.UpdatedAt) {
		t.Errorf("identical fetch = (created %v, refreshed %v, %v), want a no-op", created, again.RefreshedInPlace, err)
	}

	back, created, err := s.AddAgendaVersion(ctx, equivalentInput(meetingID, "1. Call to Order", first.SourceURL))
	if err != nil || created || !back.RefreshedInPlace || back.ContentHash != first.ContentHash || back.SourceURL != first.SourceURL {
		t.Errorf("flip back = (%+v, created %v, %v), want another refresh", back, created, err)
	}

	versions, err := s.ListAgendaVersions(ctx, meetingID)
	if err != nil || len(versions) != 1 {
		t.Errorf("versions = (%d, %v), want 1", len(versions), err)
	}
	if got := eventTypes(t, s, meetingID); !slices.Equal(got, []store.EventType{store.EventAgendaPublished}) {
		t.Errorf("events = %v, want only agenda_published", got)
	}
}

func TestEarlierPendingVersionsHoldBackLaterOnes(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-order")

	if _, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda one")); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.AddAgendaVersion(ctx, deferred(meetingID, "agenda two"))
	if err != nil {
		t.Fatal(err)
	}
	third, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda three"))
	if err != nil || !third.ChangeEventPending {
		t.Fatalf("third = (pending %v, %v), want pending behind version 2 even without DeferChangeEvent", third.ChangeEventPending, err)
	}
	if got := eventTypes(t, s, meetingID); !slices.Equal(got, []store.EventType{store.EventAgendaPublished}) {
		t.Errorf("events = %v", got)
	}

	earlier, err := s.EarlierPendingAgendaVersions(ctx, third)
	if err != nil || len(earlier) != 1 || earlier[0].ID != second.ID {
		t.Errorf("earlier pending = (%v, %v), want version 2", earlier, err)
	}
	if _, _, err := s.RecordAgendaChange(ctx, third.ID, "three", store.NoteFromDiff); !errors.Is(err, store.ErrEarlierChangePending) {
		t.Errorf("record of version 3 err = %v, want ErrEarlierChangePending", err)
	}
	if _, _, err := s.RecordAgendaChange(ctx, second.ID, "two", store.NoteFromDiff); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.RecordAgendaChange(ctx, third.ID, "three", store.NoteFromDiff); err != nil || !ok {
		t.Errorf("record of version 3 = (%v, %v)", ok, err)
	}
	events := mustEvents(t, s, meetingID)
	if len(events) != 3 || events[1].Sequence > events[2].Sequence {
		t.Fatalf("events = %+v", events)
	}
	var two, three store.AgendaEventPayload
	if json.Unmarshal(events[1].Payload, &two) != nil || json.Unmarshal(events[2].Payload, &three) != nil || two.Version != 2 || three.Version != 3 {
		t.Errorf("events out of order: %s, %s", events[1].Payload, events[2].Payload)
	}
}

func TestAgendaJobFailedBefore(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	id := uuid.New()
	const kind = "store_test_failed_kind"
	failed, err := s.AgendaJobFailedBefore(ctx, kind, id)
	if err != nil || failed {
		t.Fatalf("before = (%v, %v)", failed, err)
	}
	var jobID int64
	if err := pool.QueryRow(ctx, `
INSERT INTO river_job (kind, args, state, finalized_at, max_attempts, queue, priority)
VALUES ($1, jsonb_build_object('agenda_version_id', $2::text), 'discarded', now(), 1, 'store_test', 1)
RETURNING id`, kind, id.String()).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM river_job WHERE id = $1`, jobID) })
	if failed, err := s.AgendaJobFailedBefore(ctx, kind, id); err != nil || !failed {
		t.Errorf("after a discarded job = (%v, %v)", failed, err)
	}
}
