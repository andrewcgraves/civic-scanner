//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func savedTranscript(t *testing.T, pool *pgxpool.Pool, r store.Recording, text string) store.Transcript {
	t.Helper()
	tr, _, err := store.New(pool).SaveTranscript(t.Context(), transcriptInput(r.ID, text))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestRecordTranscriptSummaryWritesTheSummaryAndItsEventOnce(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-ready")
	tr := savedTranscript(t, pool, r, "The board approves the budget.")

	got, recorded, err := s.RecordTranscriptSummary(ctx, tr.ID, store.HashContent(tr.Text), "The board approved the budget.")
	if err != nil || !recorded || got.Summary == nil || *got.Summary != "The board approved the budget." {
		t.Fatalf("RecordTranscriptSummary = (%+v, %v, %v)", got, recorded, err)
	}
	again, recorded, err := s.RecordTranscriptSummary(ctx, tr.ID, store.HashContent(tr.Text), "A second summary.")
	if err != nil || recorded || *again.Summary != "The board approved the budget." {
		t.Errorf("second call = (%v, %v, %v), want the first summary kept and nothing recorded", *again.Summary, recorded, err)
	}

	events, err := s.ListMeetingEvents(ctx, r.MeetingID)
	if err != nil || len(events) != 1 || events[0].Type != store.EventTranscriptReady {
		t.Fatalf("events = (%+v, %v), want one transcript_ready", events, err)
	}
	var payload store.TranscriptEventPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil || payload.TranscriptID != tr.ID || payload.RecordingID != r.ID {
		t.Errorf("payload = %s (%v)", events[0].Payload, err)
	}
}

func TestRecordTranscriptSummaryRacesRecordOneEvent(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-race")
	tr := savedTranscript(t, pool, r, "Race text.")

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		recorded int
	)
	for range 8 {
		wg.Go(func() {
			_, ok, err := s.RecordTranscriptSummary(ctx, tr.ID, store.HashContent(tr.Text), "summary")
			if err != nil {
				t.Errorf("RecordTranscriptSummary: %v", err)
			}
			if ok {
				mu.Lock()
				recorded++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	events, err := s.ListMeetingEvents(ctx, r.MeetingID)
	if err != nil || recorded != 1 || len(events) != 1 {
		t.Errorf("recorded = %d, events = %d (%v), want exactly one", recorded, len(events), err)
	}
}

func TestRecordTranscriptSummaryRejectsChangedTextAndMissingRows(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-ready-changed")
	tr := savedTranscript(t, pool, r, "Old text.")

	if _, _, err := s.RecordTranscriptSummary(ctx, tr.ID, store.HashContent("Other text."), "s"); !errors.Is(err, store.ErrTranscriptChanged) {
		t.Errorf("changed text: err = %v, want ErrTranscriptChanged", err)
	}
	if _, _, err := s.RecordTranscriptSummary(ctx, uuid.New(), store.HashContent("x"), "s"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing transcript: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.RecordTranscriptSummary(ctx, tr.ID, store.ContentHash{}, "s"); !errors.Is(err, store.ErrZeroContentHash) {
		t.Errorf("zero hash: err = %v", err)
	}
	if got, err := s.GetTranscriptByID(ctx, tr.ID); err != nil || got.Summary != nil {
		t.Errorf("transcript = (%+v, %v), want it left without a summary", got, err)
	}
	if events, _ := s.ListMeetingEvents(ctx, r.MeetingID); len(events) != 0 {
		t.Errorf("events = %+v, want none", events)
	}
	if _, err := s.GetTranscriptByID(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetTranscriptByID of a missing id = %v", err)
	}
}

func TestListUnsummarizedTranscripts(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	kind := "summarize_transcript_store_test_" + uuid.NewString()
	seed := func(slug, status string, startsAt time.Time) store.Recording {
		r := seedRecording(t, pool, slug+"-"+uuid.NewString()[:8])
		if _, err := pool.Exec(ctx, `UPDATE agencies SET status = $2, website = 'https://example.com' WHERE id = (SELECT agency_id FROM meetings WHERE id = $1)`, r.MeetingID, status); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE meetings SET starts_at = $2 WHERE id = $1`, r.MeetingID, startsAt); err != nil {
			t.Fatal(err)
		}
		return r
	}
	far := time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)
	older := savedTranscript(t, pool, seed("store-test-unsummarized-older", "live", far), "older")
	newer := savedTranscript(t, pool, seed("store-test-unsummarized-newer", "live", far.Add(time.Hour)), "newer")
	done := savedTranscript(t, pool, seed("store-test-unsummarized-done", "live", far.Add(2*time.Hour)), "done")
	pending := savedTranscript(t, pool, seed("store-test-unsummarized-pending", "pending", far.Add(3*time.Hour)), "pending agency")
	failed := savedTranscript(t, pool, seed("store-test-unsummarized-failed", "live", far.Add(4*time.Hour)), "failed")
	stale := savedTranscript(t, pool, seed("store-test-unsummarized-stale", "live", far.Add(5*time.Hour)), "failed long ago")
	outside := savedTranscript(t, pool, seed("store-test-unsummarized-outside", "live", far.Add(10*time.Hour)), "outside the window")
	if _, _, err := s.RecordTranscriptSummary(ctx, done.ID, store.HashContent("done"), "s"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		id  uuid.UUID
		age string
	}{{failed.ID, "1 minute"}, {stale.ID, "8 days"}} {
		if _, err := pool.Exec(ctx, `
INSERT INTO river_job (kind, args, queue, state, finalized_at, max_attempts, attempt)
VALUES ($1, jsonb_build_object('transcript_id', $2::text), 'elsewhere', 'cancelled', now() - $3::interval, 3, 1)`, kind, f.id, f.age); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM river_job WHERE kind = $1`, kind) })

	params := store.UnsummarizedTranscriptParams{From: far, To: far.Add(6 * time.Hour), FailedKind: kind, FailedWithin: 7 * 24 * time.Hour, Limit: 10}
	ids, err := s.ListUnsummarizedTranscripts(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	want := []uuid.UUID{stale.ID, newer.ID, older.ID}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v (pending %s, failed %s, done %s, outside %s left out)", ids, want, pending.ID, failed.ID, done.ID, outside.ID)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %s, want %s", i, ids[i], want[i])
		}
	}
	params.Limit = 1
	if ids, err := s.ListUnsummarizedTranscripts(ctx, params); err != nil || len(ids) != 1 || ids[0] != stale.ID {
		t.Errorf("limit 1 = (%v, %v), want the latest meeting's transcript", ids, err)
	}
	if _, err := s.ListUnsummarizedTranscripts(ctx, store.UnsummarizedTranscriptParams{Limit: 0}); !errors.Is(err, store.ErrInvalidLimit) {
		t.Errorf("limit 0: err = %v", err)
	}
}

func TestTranscriptSummaryReadsFailOnClosedPool(t *testing.T) {
	ctx := t.Context()
	pool, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	s := store.New(pool)

	failures := map[string]error{}
	_, failures["get"] = s.GetTranscriptByID(ctx, uuid.New())
	_, _, failures["record"] = s.RecordTranscriptSummary(ctx, uuid.New(), store.HashContent("t"), "s")
	_, failures["list"] = s.ListUnsummarizedTranscripts(ctx, store.UnsummarizedTranscriptParams{Limit: 1})
	for name, err := range failures {
		if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrTranscriptChanged) {
			t.Errorf("%s: err = %v, want a connection error", name, err)
		}
	}
}
