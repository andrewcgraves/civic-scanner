//go:build integration

package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func transcriptInput(recordingID uuid.UUID, text string) store.TranscriptInput {
	return store.TranscriptInput{
		RecordingID: recordingID,
		Text:        text,
		Segments: []store.TranscriptSegment{
			{Start: 0, End: 2500 * time.Millisecond, Text: "Call to order.", Speaker: "c0/speaker_0"},
			{Start: 2500 * time.Millisecond, End: 9 * time.Second, Text: text},
		},
	}
}

func transcriptTupleID(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var tuple string
	if err := pool.QueryRow(t.Context(), `SELECT ctid::text || '/' || xmin::text FROM transcripts WHERE id = $1`, id).Scan(&tuple); err != nil {
		t.Fatal(err)
	}
	return tuple
}

func TestSaveTranscriptStoresTextAndSegments(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-save")

	in := transcriptInput(r.ID, "The board approves the budget.")
	saved, created, err := s.SaveTranscript(ctx, in)
	if err != nil || !created {
		t.Fatalf("SaveTranscript = (created %v, err %v), want created", created, err)
	}
	if saved.ID == uuid.Nil || saved.RecordingID != r.ID || saved.Text != in.Text || saved.Summary != nil ||
		!reflect.DeepEqual(saved.Segments, in.Segments) || saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Errorf("saved = %+v", saved)
	}

	got, err := s.GetTranscript(ctx, r.ID)
	if err != nil || got.ID != saved.ID || got.Text != in.Text || !reflect.DeepEqual(got.Segments, in.Segments) {
		t.Errorf("GetTranscript = (%+v, %v), want %+v", got, err, saved)
	}
}

func TestSaveTranscriptWithoutSegmentsStoresAnEmptyList(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-no-segments")

	saved, _, err := s.SaveTranscript(ctx, store.TranscriptInput{RecordingID: r.ID, Text: "text only"})
	if err != nil || saved.Segments == nil || len(saved.Segments) != 0 {
		t.Errorf("SaveTranscript = (%+v, %v), want an empty segment list", saved, err)
	}
}

func TestSaveTranscriptAgainReplacesAndKeepsSummaryOnlyForSameText(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-replace")

	first, _, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "first pass"))
	if err != nil {
		t.Fatal(err)
	}
	summarized, err := s.SetTranscriptSummary(ctx, first.ID, store.HashContent("first pass"), "summary of the first pass")
	if err != nil {
		t.Fatal(err)
	}

	resegmented := transcriptInput(r.ID, "first pass")
	resegmented.Segments = resegmented.Segments[:1]
	same, created, err := s.SaveTranscript(ctx, resegmented)
	if err != nil || created {
		t.Fatalf("same text = (created %v, err %v), want not created", created, err)
	}
	if same.ID != first.ID || len(same.Segments) != 1 || same.Summary == nil || *same.Summary != "summary of the first pass" ||
		!same.UpdatedAt.After(summarized.UpdatedAt) || !same.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("same text = %+v, want segments replaced and summary kept", same)
	}

	changed, created, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "second pass"))
	if err != nil || created {
		t.Fatalf("changed text = (created %v, err %v), want not created", created, err)
	}
	if changed.ID != first.ID || changed.Text != "second pass" || changed.Summary != nil {
		t.Errorf("changed text = %+v, want text replaced and summary cleared", changed)
	}
}

func TestSaveTranscriptUnchangedWritesNothing(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-unchanged")

	first, _, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "same"))
	if err != nil {
		t.Fatal(err)
	}
	tuple := transcriptTupleID(t, pool, first.ID)

	again, created, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "same"))
	if err != nil || created {
		t.Fatalf("unchanged save = (created %v, err %v), want not created", created, err)
	}
	if again.ID != first.ID || !again.UpdatedAt.Equal(first.UpdatedAt) || !reflect.DeepEqual(again.Segments, first.Segments) {
		t.Errorf("unchanged save = %+v, want %+v untouched", again, first)
	}
	if got := transcriptTupleID(t, pool, first.ID); got != tuple {
		t.Errorf("unchanged save wrote a new tuple: %s -> %s", tuple, got)
	}
}

func TestSaveTranscriptForMissingRecordingIsNotFound(t *testing.T) {
	s := store.New(testdb.Pool(t))

	if _, _, err := s.SaveTranscript(t.Context(), transcriptInput(uuid.New(), "orphan")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSaveTranscriptTxForMissingRecordingLeavesTheTransactionUsable(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-tx-missing")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	if _, _, err := s.SaveTranscriptTx(ctx, tx, transcriptInput(uuid.New(), "orphan")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, created, err := s.SaveTranscriptTx(ctx, tx, transcriptInput(r.ID, "kept")); err != nil || !created {
		t.Fatalf("save after ErrNotFound = (created %v, err %v), want created", created, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetTranscript(ctx, r.ID); err != nil || got.Text != "kept" {
		t.Errorf("after commit = (%+v, %v), want the saved transcript", got, err)
	}
}

func TestGetTranscriptOfRecordingWithoutOneIsNotFound(t *testing.T) {
	pool := testdb.Pool(t)
	r := seedRecording(t, pool, "store-test-transcript-none")

	if _, err := store.New(pool).GetTranscript(t.Context(), r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSetTranscriptSummary(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-summary")
	saved, _, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "Ünïcode text"))
	if err != nil {
		t.Fatal(err)
	}

	summarized, err := s.SetTranscriptSummary(ctx, saved.ID, store.HashContent(saved.Text), "The board met.")
	if err != nil {
		t.Fatalf("SetTranscriptSummary: %v", err)
	}
	if summarized.Summary == nil || *summarized.Summary != "The board met." || summarized.Text != saved.Text ||
		!reflect.DeepEqual(summarized.Segments, saved.Segments) || !summarized.UpdatedAt.After(saved.UpdatedAt) {
		t.Errorf("after summary: %+v", summarized)
	}

	if _, err := s.SetTranscriptSummary(ctx, uuid.New(), store.HashContent("x"), "s"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing transcript: err = %v, want ErrNotFound", err)
	}
}

func TestSetTranscriptSummaryOfOldTextIsRejected(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-stale-summary")

	old, _, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "old text"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "new text")); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SetTranscriptSummary(ctx, old.ID, store.HashContent(old.Text), "summary of old text"); !errors.Is(err, store.ErrTranscriptChanged) {
		t.Errorf("stale summary: err = %v, want ErrTranscriptChanged", err)
	}
	got, err := s.GetTranscript(ctx, r.ID)
	if err != nil || got.Summary != nil || got.Text != "new text" {
		t.Errorf("after stale summary = (%+v, %v), want new text without a summary", got, err)
	}
}

func TestTranscriptTxVariantsRollBackWithCaller(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-transcript-tx")
	saved, _, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "committed"))
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	if _, err := s.SetTranscriptSummaryTx(ctx, tx, saved.ID, store.HashContent("committed"), "rolled back summary"); err != nil {
		t.Fatalf("SetTranscriptSummaryTx: %v", err)
	}
	if _, _, err := s.SaveTranscriptTx(ctx, tx, transcriptInput(r.ID, "rolled back")); err != nil {
		t.Fatalf("SaveTranscriptTx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetTranscript(ctx, r.ID)
	if err != nil || got.Text != "committed" || got.Summary != nil {
		t.Errorf("after rollback: (%+v, %v), want the committed transcript without a summary", got, err)
	}
}

func TestTranscriptReadsAndWritesFailOnClosedPool(t *testing.T) {
	ctx := t.Context()
	pool, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	s := store.New(pool)

	failures := map[string]error{}
	_, _, failures["save"] = s.SaveTranscript(ctx, transcriptInput(uuid.New(), "t"))
	_, failures["get"] = s.GetTranscript(ctx, uuid.New())
	_, failures["summary"] = s.SetTranscriptSummary(ctx, uuid.New(), store.HashContent("t"), "s")
	for name, err := range failures {
		if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrTranscriptChanged) {
			t.Errorf("%s: err = %v, want a connection error", name, err)
		}
	}
}
