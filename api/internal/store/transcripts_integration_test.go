//go:build integration

package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

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
	if _, err := s.SetTranscriptSummary(ctx, first.ID, "summary of the first pass"); err != nil {
		t.Fatal(err)
	}

	resegmented := transcriptInput(r.ID, "first pass")
	resegmented.Segments = resegmented.Segments[:1]
	same, created, err := s.SaveTranscript(ctx, resegmented)
	if err != nil || created {
		t.Fatalf("same text = (created %v, err %v), want not created", created, err)
	}
	if same.ID != first.ID || len(same.Segments) != 1 || same.Summary == nil || *same.Summary != "summary of the first pass" ||
		!same.UpdatedAt.After(first.UpdatedAt) || !same.CreatedAt.Equal(first.CreatedAt) {
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

func TestSaveTranscriptForMissingRecordingIsNotFound(t *testing.T) {
	s := store.New(testdb.Pool(t))

	if _, _, err := s.SaveTranscript(t.Context(), transcriptInput(uuid.New(), "orphan")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
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
	saved, _, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "text"))
	if err != nil {
		t.Fatal(err)
	}

	summarized, err := s.SetTranscriptSummary(ctx, saved.ID, "The board met.")
	if err != nil {
		t.Fatalf("SetTranscriptSummary: %v", err)
	}
	if summarized.Summary == nil || *summarized.Summary != "The board met." || summarized.Text != "text" ||
		!reflect.DeepEqual(summarized.Segments, saved.Segments) || !summarized.UpdatedAt.After(saved.UpdatedAt) {
		t.Errorf("after summary: %+v", summarized)
	}

	if _, err := s.SetTranscriptSummary(ctx, uuid.New(), "s"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing transcript: err = %v, want ErrNotFound", err)
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
	if _, _, err := s.SaveTranscriptTx(ctx, tx, transcriptInput(r.ID, "rolled back")); err != nil {
		t.Fatalf("SaveTranscriptTx: %v", err)
	}
	if _, err := s.SetTranscriptSummaryTx(ctx, tx, saved.ID, "rolled back summary"); err != nil {
		t.Fatalf("SetTranscriptSummaryTx: %v", err)
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
	_, failures["summary"] = s.SetTranscriptSummary(ctx, uuid.New(), "s")
	for name, err := range failures {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want a connection error", name, err)
		}
	}
}
