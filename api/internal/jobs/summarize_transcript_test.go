package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/summarize"
)

const spokenTranscript = "Call to order. The board of directors meets to vote on the fare increase. The motion passes seven to two. Public comment follows."

type fakeTranscriptSummaryStore struct {
	mu        sync.Mutex
	t         store.Transcript
	getErr    error
	recordErr error
	hashes    []store.ContentHash
	recorded  int
}

func (s *fakeTranscriptSummaryStore) GetTranscriptByID(_ context.Context, id uuid.UUID) (store.Transcript, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return store.Transcript{}, s.getErr
	}
	if id != s.t.ID {
		return store.Transcript{}, store.ErrNotFound
	}
	return s.t, nil
}

func (s *fakeTranscriptSummaryStore) RecordTranscriptSummary(_ context.Context, _ uuid.UUID, hash store.ContentHash, summary string) (store.Transcript, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hashes = append(s.hashes, hash)
	if s.recordErr != nil {
		return store.Transcript{}, false, s.recordErr
	}
	if s.t.Summary != nil {
		return s.t, false, nil
	}
	s.t.Summary = &summary
	s.recorded++
	return s.t, true, nil
}

func newTranscriptSummaryStore(text string) *fakeTranscriptSummaryStore {
	return &fakeTranscriptSummaryStore{t: store.Transcript{
		ID: uuid.New(), RecordingID: uuid.New(), Text: text,
		Segments: []store.TranscriptSegment{{End: 9 * time.Second, Text: text, Speaker: "c0/speaker_0"}},
	}}
}

func transcriptFlag(on bool) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool { return on && name == FlagSummarizeTranscripts }
}

func transcriptSummaryWorker(st transcriptSummaryStore, client llm.Client) *SummarizeTranscriptWorker {
	return &SummarizeTranscriptWorker{logger: discard, store: st, llm: client, tier: llm.TierRoutine, llmTimeout: 2 * time.Minute, enabled: transcriptFlag(true)}
}

func transcriptSummaryJob(id uuid.UUID) *river.Job[SummarizeTranscriptArgs] {
	return &river.Job[SummarizeTranscriptArgs]{JobRow: &rivertype.JobRow{ID: 11, Attempt: 1}, Args: SummarizeTranscriptArgs{TranscriptID: id}}
}

func TestSummarizeTranscriptArgs(t *testing.T) {
	args := SummarizeTranscriptArgs{}
	if kind := args.Kind(); kind != "summarize_transcript" {
		t.Errorf("Kind() = %q", kind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 3 || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || len(opts.UniqueOpts.ByState) != 5 {
		t.Errorf("InsertOpts() = %+v", opts)
	}
	for _, state := range opts.UniqueOpts.ByState {
		if state == rivertype.JobStateCompleted || state == rivertype.JobStateCancelled || state == rivertype.JobStateDiscarded {
			t.Errorf("unique states include %q, which would block the sweep", state)
		}
	}
	w := transcriptSummaryWorker(nil, nil)
	if got := w.Timeout(nil); got != 19*time.Minute {
		t.Errorf("Timeout = %v, want one LLM timeout per possible call plus a minute", got)
	}
}

func TestSummarizeTranscriptWritesTheSummaryOnTheRoutineTier(t *testing.T) {
	st := newTranscriptSummaryStore(spokenTranscript)
	fake := llmtest.Returning("The board approved the fare increase.")

	if err := transcriptSummaryWorker(st, fake).Work(t.Context(), transcriptSummaryJob(st.t.ID)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if st.t.Summary == nil || *st.t.Summary != "The board approved the fare increase." || st.recorded != 1 {
		t.Errorf("summary = %v, recorded %d", st.t.Summary, st.recorded)
	}
	if len(st.hashes) != 1 || st.hashes[0] != store.HashContent(spokenTranscript) {
		t.Errorf("hashes = %v, want the hash of the text that was summarized", st.hashes)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Tier != llm.TierRoutine || !strings.Contains(calls[0].User, spokenTranscript) || strings.Contains(calls[0].User, "speaker_0") {
		t.Errorf("calls = %+v", calls)
	}

	if err := transcriptSummaryWorker(st, fake).Work(t.Context(), transcriptSummaryJob(st.t.ID)); err != nil || len(fake.Calls()) != 1 {
		t.Errorf("rerun: err %v, calls %d, want no second model call", err, len(fake.Calls()))
	}
}

func TestSummarizeTranscriptWritesTheNoSpeechNoteWithoutAModelCall(t *testing.T) {
	st := newTranscriptSummaryStore("Thank you.")
	fake := llmtest.Returning("never")
	if err := transcriptSummaryWorker(st, fake).Work(t.Context(), transcriptSummaryJob(st.t.ID)); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls()) != 0 || st.t.Summary == nil || *st.t.Summary != summarize.NoSpeechSummary || st.recorded != 1 {
		t.Errorf("calls %d, summary %v", len(fake.Calls()), st.t.Summary)
	}
}

func TestSummarizeTranscriptSkipsWithoutCallingTheModel(t *testing.T) {
	cases := map[string]struct {
		flag   bool
		client llm.Client
	}{
		"flag off":       {false, llmtest.Returning("never")},
		"no client":      {true, nil},
		"not configured": {true, llmtest.Failing(llm.ErrNotConfigured)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := newTranscriptSummaryStore(spokenTranscript)
			w := transcriptSummaryWorker(st, tc.client)
			w.enabled = transcriptFlag(tc.flag)
			if err := w.Work(t.Context(), transcriptSummaryJob(st.t.ID)); err != nil {
				t.Fatalf("Work: %v", err)
			}
			if st.t.Summary != nil || len(st.hashes) != 0 {
				t.Errorf("summary = %v, writes %d", st.t.Summary, len(st.hashes))
			}
		})
	}
}

func TestSummarizeTranscriptErrors(t *testing.T) {
	boom := errors.New("connection reset")
	cases := map[string]struct {
		setup  func(*fakeTranscriptSummaryStore)
		client llm.Client
		id     func(*fakeTranscriptSummaryStore) uuid.UUID
		cancel bool
		target error
	}{
		"missing transcript":   {client: llmtest.Returning("s"), id: func(*fakeTranscriptSummaryStore) uuid.UUID { return uuid.New() }, cancel: true, target: store.ErrNotFound},
		"read error":           {setup: func(s *fakeTranscriptSummaryStore) { s.getErr = boom }, client: llmtest.Returning("s"), target: boom},
		"retryable model":      {client: llmtest.Failing(&llm.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")})},
		"permanent model":      {client: llmtest.Failing(&llm.Error{StatusCode: 400, Err: errors.New("bad request")}), cancel: true},
		"gone before write":    {setup: func(s *fakeTranscriptSummaryStore) { s.recordErr = store.ErrNotFound }, client: llmtest.Returning("s"), cancel: true, target: store.ErrNotFound},
		"text changed":         {setup: func(s *fakeTranscriptSummaryStore) { s.recordErr = store.ErrTranscriptChanged }, client: llmtest.Returning("s"), target: store.ErrTranscriptChanged},
		"write database error": {setup: func(s *fakeTranscriptSummaryStore) { s.recordErr = boom }, client: llmtest.Returning("s"), target: boom},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := newTranscriptSummaryStore(spokenTranscript)
			if tc.setup != nil {
				tc.setup(st)
			}
			id := st.t.ID
			if tc.id != nil {
				id = tc.id(st)
			}
			err := transcriptSummaryWorker(st, tc.client).Work(t.Context(), transcriptSummaryJob(id))
			var cancel *river.JobCancelError
			if err == nil || errors.As(err, &cancel) != tc.cancel || (tc.target != nil && !errors.Is(err, tc.target)) {
				t.Errorf("err = %v, want cancel %v, target %v", err, tc.cancel, tc.target)
			}
			if st.recorded != 0 {
				t.Errorf("recorded %d summaries", st.recorded)
			}
		})
	}
}

func TestEnqueueSummarizeTranscriptNeverFailsTheTranscript(t *testing.T) {
	tr := store.Transcript{ID: uuid.New(), RecordingID: uuid.New()}
	if err := (EnqueueSummarizeTranscript{Enabled: transcriptFlag(false)}).TranscriptSaved(t.Context(), nil, tr); err != nil {
		t.Errorf("flag off: %v", err)
	}
	if err := (EnqueueSummarizeTranscript{}).TranscriptSaved(t.Context(), nil, tr); err != nil {
		t.Errorf("no flag function: %v", err)
	}
	if err := (EnqueueSummarizeTranscript{Enabled: transcriptFlag(true)}).TranscriptSaved(t.Context(), nil, tr); err != nil {
		t.Errorf("no River client in the context: %v, want it logged and nil", err)
	}
}

type fakeTranscriptSummarySweepStore struct {
	ids   []uuid.UUID
	err   error
	calls []store.UnsummarizedTranscriptParams
}

func (s *fakeTranscriptSummarySweepStore) ListUnsummarizedTranscripts(_ context.Context, p store.UnsummarizedTranscriptParams) ([]uuid.UUID, error) {
	s.calls = append(s.calls, p)
	return s.ids, s.err
}

func transcriptSummarySweepWorker(st *fakeTranscriptSummarySweepStore) *EnqueueTranscriptSummariesWorker {
	return &EnqueueTranscriptSummariesWorker{
		logger: discard, store: st, clock: sourcestest.NewClock(sweepNow), llm: llmtest.Returning("s"), enabled: transcriptFlag(true), queue: "ingest",
	}
}

func transcriptSummarySweepJob() *river.Job[EnqueueTranscriptSummariesArgs] {
	return &river.Job[EnqueueTranscriptSummariesArgs]{JobRow: &rivertype.JobRow{ID: 12}}
}

func TestEnqueueTranscriptSummariesArgs(t *testing.T) {
	args := EnqueueTranscriptSummariesArgs{}
	if kind := args.Kind(); kind != "enqueue_transcript_summaries" {
		t.Errorf("Kind() = %q", kind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 5 || !opts.UniqueOpts.ByQueue || !opts.UniqueOpts.ByArgs {
		t.Errorf("InsertOpts() = %+v", opts)
	}
	gotArgs, gotOpts := periodicTranscriptSummarySweep(sourcestest.NewClock(sweepNow), "ingest")()
	if got, ok := gotArgs.(EnqueueTranscriptSummariesArgs); !ok || !got.Slot.Equal(time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)) || gotOpts.Queue != "ingest" {
		t.Errorf("periodic constructor = %#v, %+v", gotArgs, gotOpts)
	}
}

func TestTranscriptSweepDoesNothingWhenOffOrUnconfigured(t *testing.T) {
	for name, w := range map[string]func(*EnqueueTranscriptSummariesWorker){
		"flag off":  func(w *EnqueueTranscriptSummariesWorker) { w.enabled = transcriptFlag(false) },
		"no client": func(w *EnqueueTranscriptSummariesWorker) { w.llm = nil },
	} {
		st := &fakeTranscriptSummarySweepStore{ids: []uuid.UUID{uuid.New()}}
		worker := transcriptSummarySweepWorker(st)
		w(worker)
		if err := worker.Work(t.Context(), transcriptSummarySweepJob()); err != nil || len(st.calls) != 0 {
			t.Errorf("%s: err %v, store calls %d", name, err, len(st.calls))
		}
	}
}

func TestTranscriptSweepAsksForOwedTranscriptsWithBackoff(t *testing.T) {
	st := &fakeTranscriptSummarySweepStore{}
	if err := transcriptSummarySweepWorker(st).Work(t.Context(), transcriptSummarySweepJob()); err != nil {
		t.Fatal(err)
	}
	want := store.UnsummarizedTranscriptParams{
		From:       time.Date(2025, 11, 18, 9, 30, 0, 0, time.UTC),
		To:         time.Date(2027, 2, 16, 9, 30, 0, 0, time.UTC),
		FailedKind: "summarize_transcript", FailedWithin: 7 * 24 * time.Hour, Limit: 10,
	}
	if len(st.calls) != 1 || st.calls[0] != want {
		t.Errorf("calls = %+v, want %+v", st.calls, want)
	}

	st.err = errors.New("db down")
	if err := transcriptSummarySweepWorker(st).Work(t.Context(), transcriptSummarySweepJob()); !errors.Is(err, st.err) {
		t.Errorf("store error: %v", err)
	}
	st.err, st.ids = nil, []uuid.UUID{uuid.New()}
	if err := transcriptSummarySweepWorker(st).Work(t.Context(), transcriptSummarySweepJob()); err == nil {
		t.Error("no River client in the context: err = nil")
	}
}
