package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	return r.lines(slog.LevelError, message)
}

func (r *logRecorder) lines(level slog.Level, message string) []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]string
	for _, rec := range r.records {
		if rec.Level != level || rec.Message != message {
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

const breakerKey = "gateway anthropic/claude-haiku-5.5"

func admit(t *testing.T, b *llmBreaker) admission {
	t.Helper()
	a, ok, _ := b.allow(breakerKey)
	if !ok {
		t.Fatal("call refused")
	}
	return a
}

func refused(b *llmBreaker) bool {
	_, ok, _ := b.allow(breakerKey)
	return !ok
}

func openBreaker(t *testing.T) (*llmBreaker, *sourcestest.Clock) {
	t.Helper()
	clock := sourcestest.NewClock(breakerNow)
	b := newLLMBreaker(clock, 10*time.Minute)
	b.record(admit(t, b), accountError())
	return b, clock
}

func TestBreakerOpensOnAnAccountFailureForTheCooldown(t *testing.T) {
	b, clock := openBreaker(t)
	clock.Advance(4 * time.Minute)
	if _, ok, wait := b.allow(breakerKey); ok || wait != 6*time.Minute {
		t.Fatalf("allow = %v, %v, want refused for the remaining 6m", ok, wait)
	}
	clock.Advance(6*time.Minute - time.Nanosecond)
	if !refused(b) {
		t.Fatal("allowed a call before the cool-down ended")
	}
}

func TestBreakerLetsOneProbeThroughAfterTheCooldown(t *testing.T) {
	b, clock := openBreaker(t)
	clock.Advance(10 * time.Minute)

	probe := admit(t, b)
	if _, ok, wait := b.allow(breakerKey); ok || wait != 10*time.Minute {
		t.Fatalf("second call while the probe is out: allow = %v, %v, want refused", ok, wait)
	}
	b.record(probe, accountError())
	if !refused(b) {
		t.Fatal("a failed probe did not reopen the breaker")
	}
	clock.Advance(10 * time.Minute)
	b.record(admit(t, b), nil)
	for range 3 {
		if refused(b) {
			t.Fatal("a successful probe did not close the breaker")
		}
	}
}

func TestBreakerReleasesAProbeThatNeverReported(t *testing.T) {
	b, clock := openBreaker(t)
	clock.Advance(10 * time.Minute)
	lost := admit(t, b)
	clock.Advance(10 * time.Minute)
	second := admit(t, b)

	b.record(lost, nil)
	if !refused(b) {
		t.Fatal("the lost probe's late result closed the breaker while the second probe is out")
	}
	b.record(second, nil)
	if refused(b) {
		t.Fatal("the second probe's success did not close the breaker")
	}
}

func TestBreakerClosesOnAnyOtherProbeOutcome(t *testing.T) {
	for name, err := range map[string]error{
		"success":     nil,
		"bad request": &llm.Error{StatusCode: 400, Err: errors.New("bad")},
		"rate limit":  &llm.Error{StatusCode: 429, Retryable: true, Err: errors.New("slow")},
		"timeout":     &llm.Error{Retryable: true, Err: context.DeadlineExceeded},
		"truncated":   llm.ErrTruncated,
	} {
		t.Run(name, func(t *testing.T) {
			b, clock := openBreaker(t)
			clock.Advance(10 * time.Minute)
			b.record(admit(t, b), err)
			for range 2 {
				if refused(b) {
					t.Fatal("breaker still open or probing")
				}
			}
		})
	}
}

func TestBreakerIgnoresALateResultFromACallAdmittedBeforeItOpened(t *testing.T) {
	clock := sourcestest.NewClock(breakerNow)
	b := newLLMBreaker(clock, 10*time.Minute)
	inFlight := admit(t, b)
	failing := admit(t, b)
	b.record(failing, accountError())

	b.record(inFlight, &llm.Error{Retryable: true, Err: errors.New("timeout")})
	if !refused(b) {
		t.Fatal("a stale non-account result from a call started before the breaker opened closed it")
	}
	b.record(inFlight, nil)
	if !refused(b) {
		t.Fatal("a stale success closed the breaker")
	}
}

func TestBreakerStrayResultNeverClearsAnInFlightProbe(t *testing.T) {
	clock := sourcestest.NewClock(breakerNow)
	b := newLLMBreaker(clock, 10*time.Minute)
	stray := admit(t, b)
	b.record(admit(t, b), accountError())
	clock.Advance(10 * time.Minute)
	probe := admit(t, b)

	b.record(stray, &llm.Error{Retryable: true, Err: errors.New("context canceled")})
	if !refused(b) {
		t.Fatal("second call allowed while the first probe is still in flight")
	}
	b.record(probe, nil)
	if refused(b) {
		t.Fatal("the probe's own success did not close the breaker")
	}
}

func TestBreakerReopensOnALateAccountFailure(t *testing.T) {
	clock := sourcestest.NewClock(breakerNow)
	b := newLLMBreaker(clock, 10*time.Minute)
	late := admit(t, b)
	b.record(admit(t, b), accountError())
	clock.Advance(10 * time.Minute)
	probe := admit(t, b)
	b.record(late, accountError())
	b.record(probe, nil)
	if !refused(b) {
		t.Fatal("a probe admitted before the latest account failure closed the breaker")
	}
}

func TestBreakerKeysAreIndependent(t *testing.T) {
	b, _ := openBreaker(t)
	if _, ok, _ := b.allow("gateway anthropic/claude-sonnet-5.5"); !ok {
		t.Fatal("a failure on one model paused another")
	}
}

type gatedLLM struct {
	calls atomic.Int64
	gate  chan struct{}
	err   error
}

func (g *gatedLLM) Complete(context.Context, llm.Request) (llm.Response, error) {
	g.calls.Add(1)
	<-g.gate
	return llm.Response{}, g.err
}

func TestBreakerLetsExactlyOneProbeThroughUnderConcurrency(t *testing.T) {
	for range 50 {
		clock := sourcestest.NewClock(breakerNow)
		b := newLLMBreaker(clock, 10*time.Minute)
		opening, _, _ := b.allow("tier " + string(llm.TierRoutine))
		b.record(opening, accountError())
		clock.Advance(10 * time.Minute)
		inner := &gatedLLM{gate: make(chan struct{}), err: accountError()}
		guarded := guardLLM(inner, b)
		var wg sync.WaitGroup
		var paused atomic.Int64
		start := make(chan struct{})
		for range 64 {
			wg.Go(func() {
				<-start
				if _, err := guarded.Complete(context.Background(), llm.Request{Tier: llm.TierRoutine}); isLLMPaused(err) {
					paused.Add(1)
				}
			})
		}
		close(start)
		for paused.Load() < 63 && inner.calls.Load() < 2 {
			runtime.Gosched()
		}
		close(inner.gate)
		wg.Wait()
		if inner.calls.Load() != 1 || paused.Load() != 63 {
			t.Fatalf("calls = %d, paused = %d, want 1 and 63", inner.calls.Load(), paused.Load())
		}
	}
}

type routedLLM struct {
	*llmtest.Fake
	models map[llm.Tier]string
}

func (r routedLLM) Route(tier llm.Tier) (string, string) { return "anthropic", r.models[tier] }

func TestGuardedLLMKeysTheBreakerByProviderAndModel(t *testing.T) {
	fake := &llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		if req.Tier == llm.TierStrong {
			return llm.Response{}, accountError()
		}
		return llm.Response{Text: "ok", FinishReason: "stop"}, nil
	}}
	guarded, _ := breakerFor(routedLLM{Fake: fake, models: map[llm.Tier]string{llm.TierRoutine: "claude-haiku-5-5", llm.TierStrong: "claude-sonnet-5-5"}})

	if _, err := guarded.Complete(t.Context(), llm.Request{Tier: llm.TierStrong}); !llm.IsAccountFailure(err) {
		t.Fatalf("strong: err = %v", err)
	}
	_, err := guarded.Complete(t.Context(), llm.Request{Tier: llm.TierStrong})
	if !isLLMPaused(err) || !strings.Contains(err.Error(), "anthropic claude-sonnet-5-5") {
		t.Fatalf("strong again: err = %v, want paused for that model", err)
	}
	for range 3 {
		if _, err := guarded.Complete(t.Context(), llm.Request{Tier: llm.TierRoutine}); err != nil {
			t.Fatalf("routine: err = %v, want calls to the healthy model to flow", err)
		}
	}
	if n := len(fake.Calls()); n != 4 {
		t.Errorf("model calls = %d, want 4", n)
	}
}

func TestGuardedLLMSharesTheBreakerWhenTiersShareAModel(t *testing.T) {
	fake := llmtest.Failing(accountError())
	guarded, _ := breakerFor(routedLLM{Fake: fake, models: map[llm.Tier]string{llm.TierRoutine: "claude-haiku-5-5", llm.TierStrong: "claude-haiku-5-5"}})

	_, _ = guarded.Complete(t.Context(), llm.Request{Tier: llm.TierStrong})
	if _, err := guarded.Complete(t.Context(), llm.Request{Tier: llm.TierRoutine}); !isLLMPaused(err) {
		t.Errorf("err = %v, want the shared model paused", err)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
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
	if _, err := guarded.Complete(t.Context(), llm.Request{Tier: llm.TierStrong}); !llm.IsAccountFailure(err) {
		t.Errorf("strong tier: err = %v, want its own call when the client names no models", err)
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
	var reasons []string
	for _, line := range logs.lines(slog.LevelInfo, "agenda change recorded") {
		reasons = append(reasons, line["reason"])
	}
	if !slices.Equal(reasons, []string{"llm_account_failure", "llm_paused"}) {
		t.Errorf("reasons = %v, want llm_account_failure then llm_paused", reasons)
	}
}

func TestChangeNoteUsesTheModelWhenOnlyAnotherModelIsPaused(t *testing.T) {
	fake := &llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		if req.Tier == llm.TierStrong {
			return llm.Response{}, accountError()
		}
		return llm.Response{Text: "The contract rose.", FinishReason: "stop"}, nil
	}}
	client, _ := breakerFor(routedLLM{Fake: fake, models: map[llm.Tier]string{llm.TierRoutine: "claude-haiku-5-5", llm.TierStrong: "claude-sonnet-5-5"}})
	if _, err := client.Complete(t.Context(), llm.Request{Tier: llm.TierStrong}); !llm.IsAccountFailure(err) {
		t.Fatalf("strong: err = %v", err)
	}
	v1, v2 := changePair()
	st := newFakeChangeStore(v1, v2)
	logs := &logRecorder{}
	w := changeWorker(st, client)
	w.logger = slog.New(logs)

	if err := w.Work(t.Context(), changeJob(v2.ID, 1)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := st.recorded(); len(got) != 1 || got[0].source != store.NoteFromModel || got[0].note != "The contract rose." {
		t.Errorf("recorded = %+v, want the model's note on the healthy routine model", got)
	}
	if lines := logs.lines(slog.LevelInfo, "agenda change recorded"); len(lines) != 1 || lines[0]["reason"] != "model" {
		t.Errorf("recorded lines = %v, want reason model", lines)
	}
}
