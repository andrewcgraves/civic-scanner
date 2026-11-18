package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

type fakeSweepStore struct {
	ids   []uuid.UUID
	err   error
	calls []store.UnsummarizedParams
}

func (s *fakeSweepStore) ListUnsummarizedAgendaVersions(_ context.Context, p store.UnsummarizedParams) ([]uuid.UUID, error) {
	s.calls = append(s.calls, p)
	return s.ids, s.err
}

var sweepNow = time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)

func sweepWorker(st *fakeSweepStore) *EnqueueAgendaSummariesWorker {
	return &EnqueueAgendaSummariesWorker{
		logger:  discard,
		store:   st,
		clock:   sourcestest.NewClock(sweepNow),
		llm:     llmtest.Returning("s"),
		enabled: summaryFlag(true),
		queue:   "ingest",
	}
}

func sweepJob() *river.Job[EnqueueAgendaSummariesArgs] {
	return &river.Job[EnqueueAgendaSummariesArgs]{JobRow: &rivertype.JobRow{ID: 9}}
}

func TestEnqueueAgendaSummariesArgs(t *testing.T) {
	args := EnqueueAgendaSummariesArgs{}
	if kind := args.Kind(); kind != "enqueue_agenda_summaries" {
		t.Errorf("Kind() = %q", kind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 5 || !opts.UniqueOpts.ByQueue || !opts.UniqueOpts.ByArgs {
		t.Errorf("InsertOpts() = %+v", opts)
	}
	gotArgs, gotOpts := periodicSummarySweep(sourcestest.NewClock(sweepNow), "ingest")()
	if got, ok := gotArgs.(EnqueueAgendaSummariesArgs); !ok || !got.Slot.Equal(time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)) || gotOpts.Queue != "ingest" {
		t.Errorf("periodic constructor = %#v, %+v", gotArgs, gotOpts)
	}
}

func TestSweepDoesNothingWhenSummariesCannotRun(t *testing.T) {
	off := &fakeSweepStore{ids: []uuid.UUID{uuid.New()}}
	w := sweepWorker(off)
	w.enabled = summaryFlag(false)
	if err := w.Work(t.Context(), sweepJob()); err != nil || len(off.calls) != 0 {
		t.Errorf("flag off: Work = %v, store calls %d", err, len(off.calls))
	}

	noLLM := &fakeSweepStore{ids: []uuid.UUID{uuid.New()}}
	w = sweepWorker(noLLM)
	w.llm = nil
	if err := w.Work(t.Context(), sweepJob()); err != nil || len(noLLM.calls) != 0 {
		t.Errorf("no LLM: Work = %v, store calls %d", err, len(noLLM.calls))
	}
}

func TestSweepAsksForTheIngestWindowWithABackoffAndALimit(t *testing.T) {
	st := &fakeSweepStore{}
	if err := sweepWorker(st).Work(t.Context(), sweepJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	want := store.UnsummarizedParams{
		From:         sweepNow.AddDate(0, 0, -30),
		To:           sweepNow.AddDate(0, 0, 90),
		FailedKind:   "summarize_agenda",
		FailedWithin: 7 * 24 * time.Hour,
		Limit:        50,
	}
	if len(st.calls) != 1 || st.calls[0] != want {
		t.Errorf("params = %+v, want %+v", st.calls, want)
	}
}

func TestSweepRetriesStoreErrorsAndNeedsARiverClient(t *testing.T) {
	boom := errors.New("connection reset")
	if err := sweepWorker(&fakeSweepStore{err: boom}).Work(t.Context(), sweepJob()); !errors.Is(err, boom) || isCancel(err) {
		t.Errorf("store error: Work = %v, want the retryable error", err)
	}
	if err := sweepWorker(&fakeSweepStore{ids: []uuid.UUID{uuid.New()}}).Work(t.Context(), sweepJob()); err == nil {
		t.Error("outside a River job: Work returned no error")
	}
}
