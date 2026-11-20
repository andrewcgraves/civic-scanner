package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
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
)

var breakerNow = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

func accountError() *llm.Error {
	return &llm.Error{
		StatusCode: 403, Account: true, Provider: "gateway", Model: "anthropic/claude-haiku-5.5",
		Type: "permission_error", Code: "model_not_allowed", Err: errors.New("gateway returned 403: SECRET-BODY-TEXT"),
	}
}

type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}

func (r *logRecorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &boundRecorder{parent: r, attrs: attrs}
}

func (r *logRecorder) WithGroup(string) slog.Handler { return r }

func (r *logRecorder) errors(message string) []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]string
	for _, rec := range r.records {
		if rec.Level != slog.LevelError || rec.Message != message {
			continue
		}
		fields := map[string]string{}
		rec.Attrs(func(a slog.Attr) bool {
			fields[a.Key] = a.Value.String()
			return true
		})
		out = append(out, fields)
	}
	return out
}

func (r *logRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b bytes.Buffer
	for _, rec := range r.records {
		b.WriteString(rec.Message)
		rec.Attrs(func(a slog.Attr) bool {
			b.WriteString(" " + a.Key + "=" + a.Value.String())
			return true
		})
		b.WriteString("\n")
	}
	return b.String()
}

type boundRecorder struct {
	parent *logRecorder
	attrs  []slog.Attr
}

func (b *boundRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (b *boundRecorder) Handle(ctx context.Context, rec slog.Record) error {
	rec = rec.Clone()
	rec.AddAttrs(b.attrs...)
	return b.parent.Handle(ctx, rec)
}

func (b *boundRecorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &boundRecorder{parent: b.parent, attrs: append(slices.Clone(b.attrs), attrs...)}
}

func (b *boundRecorder) WithGroup(string) slog.Handler { return b }

func snoozeOf(t *testing.T, err error) time.Duration {
	t.Helper()
	var snooze *rivertype.JobSnoozeError
	if !errors.As(err, &snooze) {
		t.Fatalf("err = %v, want a snooze", err)
	}
	return snooze.Duration
}

func TestBreakerOpensOnAnAccountFailureForTheCooldown(t *testing.T) {
	clock := sourcestest.NewClock(breakerNow)
	b := newLLMBreaker(clock, 10*time.Minute)

	if ok, _ := b.allow(); !ok {
		t.Fatal("a fresh breaker refused a call")
	}
	b.record(accountError())
	clock.Advance(4 * time.Minute)
	if ok, wait := b.allow(); ok || wait != 6*time.Minute {
		t.Fatalf("allow = %v, %v, want refused for the remaining 6m", ok, wait)
	}
	clock.Advance(6*time.Minute - time.Nanosecond)
	if ok, _ := b.allow(); ok {
		t.Fatal("allowed a call before the cool-down ended")
	}
}

func TestBreakerLetsOneProbeThroughAfterTheCooldown(t *testing.T) {
	clock := sourcestest.NewClock(breakerNow)
	b := newLLMBreaker(clock, 10*time.Minute)
	b.record(accountError())
	clock.Advance(10 * time.Minute)

	if ok, _ := b.allow(); !ok {
		t.Fatal("no probe after the cool-down")
	}
	if ok, wait := b.allow(); ok || wait != 10*time.Minute {
		t.Fatalf("second call while the probe is out: allow = %v, %v, want refused", ok, wait)
	}

	b.record(accountError())
	if ok, _ := b.allow(); ok {
		t.Fatal("a failed probe did not reopen the breaker")
	}
	clock.Advance(10 * time.Minute)
	if ok, _ := b.allow(); !ok {
		t.Fatal("no second probe after the next cool-down")
	}
	b.record(nil)
	for range 3 {
		if ok, _ := b.allow(); !ok {
			t.Fatal("a successful probe did not close the breaker")
		}
	}
}

func TestBreakerReleasesAProbeThatNeverReported(t *testing.T) {
	clock := sourcestest.NewClock(breakerNow)
	b := newLLMBreaker(clock, 10*time.Minute)
	b.record(accountError())
	clock.Advance(10 * time.Minute)
	if ok, _ := b.allow(); !ok {
		t.Fatal("no probe")
	}
	clock.Advance(10 * time.Minute)
	if ok, _ := b.allow(); !ok {
		t.Fatal("a lost probe blocked calls past its own cool-down")
	}
}

func TestBreakerClosesOnAnyOtherOutcome(t *testing.T) {
	for name, err := range map[string]error{
		"success":     nil,
		"bad request": &llm.Error{StatusCode: 400, Err: errors.New("bad")},
		"rate limit":  &llm.Error{StatusCode: 429, Retryable: true, Err: errors.New("slow")},
		"truncated":   llm.ErrTruncated,
	} {
		t.Run(name, func(t *testing.T) {
			clock := sourcestest.NewClock(breakerNow)
			b := newLLMBreaker(clock, 10*time.Minute)
			b.record(accountError())
			clock.Advance(10 * time.Minute)
			b.allow()
			b.record(err)
			if ok, _ := b.allow(); !ok {
				t.Error("breaker still open")
			}
			if ok, _ := b.allow(); !ok {
				t.Error("breaker still probing")
			}
		})
	}
}

func TestGuardedLLMSkipsTheModelWhileOpen(t *testing.T) {
	clock := sourcestest.NewClock(breakerNow)
	b := newLLMBreaker(clock, 10*time.Minute)
	failing := llmtest.Failing(accountError())
	guarded := guardLLM(failing, b)

	if _, err := guarded.Complete(t.Context(), llm.Request{Tier: llm.TierRoutine}); !llm.IsAccountFailure(err) {
		t.Fatalf("err = %v, want the account failure", err)
	}
	_, err := guarded.Complete(t.Context(), llm.Request{Tier: llm.TierRoutine})
	if !isLLMPaused(err) || llm.IsRetryable(err) || llm.IsAccountFailure(err) || !strings.Contains(err.Error(), "paused for 10m0s") {
		t.Fatalf("err = %v, want the paused error", err)
	}
	if n := len(failing.Calls()); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
	if guardLLM(nil, b) != nil {
		t.Error("guarding no client must keep no client")
	}
}

func breakerFor(client llm.Client) (llm.Client, *sourcestest.Clock) {
	clock := sourcestest.NewClock(breakerNow)
	return guardLLM(client, newLLMBreaker(clock, llmAccountCooldown)), clock
}

func TestAgendaSummaryAccountFailureSnoozesAndLogsOneError(t *testing.T) {
	first := agendaVersion(uuid.New(), 1, summaryAgendaText)
	second := agendaVersion(uuid.New(), 1, summaryAgendaText+"\n3. Adjourn")
	st := newFakeSummaryStore(first, second)
	var fail bool
	fake := &llmtest.Fake{Respond: func(llm.Request) (llm.Response, error) {
		if fail {
			return llm.Response{}, accountError()
		}
		return llm.Response{Text: "The board adopts its budget.", FinishReason: "stop"}, nil
	}}
	fail = true
	client, clock := breakerFor(fake)
	logs := &logRecorder{}
	w := summaryWorker(st, client)
	w.logger = slog.New(logs)

	if got := snoozeOf(t, w.Work(t.Context(), summaryJob(first.ID))); got != time.Hour {
		t.Errorf("snooze = %v, want 1h", got)
	}
	errs := logs.errors(LLMAccountFailureMessage)
	if len(errs) != 1 {
		t.Fatalf("error lines = %d, want 1:\n%s", len(errs), logs.text())
	}
	for k, want := range map[string]string{
		"provider": "gateway", "model": "anthropic/claude-haiku-5.5", "status": "403", "error_type": "permission_error",
		"error_code": "model_not_allowed", "agenda_version_id": first.ID.String(), "snooze": "1h0m0s",
	} {
		if errs[0][k] != want {
			t.Errorf("%s = %q, want %q", k, errs[0][k], want)
		}
	}
	if strings.Contains(logs.text(), "SECRET-BODY-TEXT") {
		t.Errorf("the provider's message reached the log:\n%s", logs.text())
	}

	if got := snoozeOf(t, w.Work(t.Context(), summaryJob(second.ID))); got != llmAccountCooldown {
		t.Errorf("snooze during the cool-down = %v, want %v", got, llmAccountCooldown)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("model calls = %d, want 1: the breaker must stop the second", n)
	}
	if n := len(logs.errors(LLMAccountFailureMessage)); n != 1 {
		t.Errorf("error lines = %d, want still 1", n)
	}

	fail = false
	clock.Advance(llmAccountCooldown)
	for _, v := range []store.AgendaVersion{second, first} {
		if err := w.Work(t.Context(), summaryJob(v.ID)); err != nil {
			t.Fatalf("Work after recovery: %v", err)
		}
		if st.summary(v.ID) == nil {
			t.Errorf("version %s not summarized after recovery", v.ID)
		}
	}
}

func TestAgendaSummaryPausedSnoozeHasAFloor(t *testing.T) {
	v := agendaVersion(uuid.New(), 1, summaryAgendaText)
	client, clock := breakerFor(llmtest.Failing(accountError()))
	w := summaryWorker(newFakeSummaryStore(v), client)
	snoozeOf(t, w.Work(t.Context(), summaryJob(v.ID)))
	clock.Advance(llmAccountCooldown - time.Second)

	if got := snoozeOf(t, w.Work(t.Context(), summaryJob(v.ID))); got != llmPausedMinSnooze {
		t.Errorf("snooze = %v, want the %v floor", got, llmPausedMinSnooze)
	}
}

func TestAgendaSummaryPerItemFailuresStillCancel(t *testing.T) {
	for name, err := range map[string]error{
		"bad request": &llm.Error{StatusCode: 400, Err: errors.New("bad")},
		"too large":   &llm.Error{StatusCode: 413, Err: errors.New("big")},
		"refusal":     llm.ErrContentFiltered,
		"truncated":   llm.ErrTruncated,
	} {
		t.Run(name, func(t *testing.T) {
			v := agendaVersion(uuid.New(), 1, summaryAgendaText)
			client, _ := breakerFor(llmtest.Failing(err))
			logs := &logRecorder{}
			w := summaryWorker(newFakeSummaryStore(v), client)
			w.logger = slog.New(logs)

			got := w.Work(t.Context(), summaryJob(v.ID))
			var cancel *river.JobCancelError
			if !errors.As(got, &cancel) || !errors.Is(got, err) {
				t.Errorf("err = %v, want a cancel", got)
			}
			if n := len(logs.errors(LLMAccountFailureMessage)); n != 0 {
				t.Errorf("error lines = %d, want 0", n)
			}
		})
	}
}

func TestTranscriptSummaryAccountFailureSnoozesAndTripsTheBreaker(t *testing.T) {
	st := newTranscriptSummaryStore(spokenTranscript)
	fake := llmtest.Failing(accountError())
	client, _ := breakerFor(fake)
	logs := &logRecorder{}
	w := transcriptSummaryWorker(st, client)
	w.logger = slog.New(logs)

	if got := snoozeOf(t, w.Work(t.Context(), transcriptSummaryJob(st.t.ID))); got != time.Hour {
		t.Errorf("snooze = %v, want 1h", got)
	}
	if got := snoozeOf(t, w.Work(t.Context(), transcriptSummaryJob(st.t.ID))); got != llmAccountCooldown {
		t.Errorf("snooze during the cool-down = %v, want %v", got, llmAccountCooldown)
	}
	errs := logs.errors(LLMAccountFailureMessage)
	if len(errs) != 1 || errs[0]["transcript_id"] != st.t.ID.String() || errs[0]["status"] != "403" {
		t.Errorf("error lines = %v, want one for the transcript", errs)
	}
	if len(fake.Calls()) != 1 || st.recorded != 0 {
		t.Errorf("calls %d, recorded %d, want 1 and 0", len(fake.Calls()), st.recorded)
	}
}

func TestChangeNoteAccountFailureFallsBackToTheWordDiffAndTripsTheBreaker(t *testing.T) {
	v1, v2 := changePair()
	st := newFakeChangeStore(v1, v2)
	fake := llmtest.Failing(accountError())
	client, _ := breakerFor(fake)
	logs := &logRecorder{}
	w := changeWorker(st, client)
	w.logger = slog.New(logs)

	if err := w.Work(t.Context(), changeJob(v2.ID, 1)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := st.recorded(); len(got) != 1 || got[0].source != store.NoteFromDiff {
		t.Errorf("recorded = %+v, want the word-diff note", got)
	}
	if errs := logs.errors(LLMAccountFailureMessage); len(errs) != 1 || errs[0]["agenda_version_id"] != v2.ID.String() {
		t.Errorf("error lines = %v, want one", errs)
	}

	v3, v4 := changePair()
	st2 := newFakeChangeStore(v3, v4)
	w.store = st2
	if err := w.Work(t.Context(), changeJob(v4.ID, 1)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("model calls = %d, want 1: the breaker must stop the second", n)
	}
	if got := st2.recorded(); len(got) != 1 || got[0].source != store.NoteFromDiff {
		t.Errorf("recorded = %+v, want the word-diff note without a call", got)
	}
	if n := len(logs.errors(LLMAccountFailureMessage)); n != 1 {
		t.Errorf("error lines = %d, want still 1", n)
	}
}
