package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const ownerFocus = "Student fares on the A Line"

type fakePromptStore struct {
	delivery   store.WebhookDelivery
	err        error
	agenda     store.AgendaVersion
	agendaErr  error
	transcript store.Transcript
	runs       []store.DirectedRunInput
	recordErr  error
}

func (s *fakePromptStore) WebhookDelivery(context.Context, uuid.UUID, uuid.UUID) (store.WebhookDelivery, error) {
	return s.delivery, s.err
}

func (s *fakePromptStore) GetAgendaVersion(context.Context, uuid.UUID) (store.AgendaVersion, error) {
	return s.agenda, s.agendaErr
}

func (s *fakePromptStore) GetTranscriptByID(context.Context, uuid.UUID) (store.Transcript, error) {
	return s.transcript, nil
}

func (s *fakePromptStore) RecordDirectedRun(_ context.Context, in store.DirectedRunInput) (store.DirectedRun, error) {
	s.runs = append(s.runs, in)
	return store.DirectedRun{}, s.recordErr
}

func (s *fakePromptStore) RecordDirectedRunTx(ctx context.Context, _ pgx.Tx, in store.DirectedRunInput) (store.DirectedRun, error) {
	return s.RecordDirectedRun(ctx, in)
}

func directedFixture(t *testing.T) store.WebhookDelivery {
	t.Helper()
	d := deliveryFixture(t, testWebhookSealer(t))
	focus := ownerFocus
	d.Prompt = &focus
	return d
}

func promptWorker(st *fakePromptStore, client llm.Client, logs *bytes.Buffer) *RunPaidPromptsWorker {
	logger := discard
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return &RunPaidPromptsWorker{
		logger:  logger,
		store:   st,
		llm:     client,
		enabled: func(context.Context, string) bool { return true },
	}
}

func promptJob(attempt int) *river.Job[RunPaidPromptsArgs] {
	return &river.Job[RunPaidPromptsArgs]{JobRow: &rivertype.JobRow{ID: 9, Attempt: attempt, MaxAttempts: directedPromptAttempts}}
}

func TestRunPaidPromptsArgs(t *testing.T) {
	if kind := (RunPaidPromptsArgs{}).Kind(); kind != "run_paid_prompts" {
		t.Errorf("Kind() = %q", kind)
	}
	if opts := (RunPaidPromptsArgs{}).InsertOpts(); opts.MaxAttempts != directedPromptAttempts || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Errorf("InsertOpts() = %+v", opts)
	}
	if got := (&RunPaidPromptsWorker{llmTimeout: defaultLLMTimeout}).Timeout(nil); got != defaultLLMTimeout+summarizeTimeoutMargin {
		t.Errorf("Timeout = %v", got)
	}
}

func TestRunPaidPromptsSkipsWhatShouldNotBeRead(t *testing.T) {
	for name, change := range map[string]func(*fakePromptStore, *RunPaidPromptsWorker){
		"gone":   func(s *fakePromptStore, _ *RunPaidPromptsWorker) { s.err = store.ErrNotFound },
		"paused": func(s *fakePromptStore, _ *RunPaidPromptsWorker) { s.delivery.Status = store.WebhookPaused },
		"free plan": func(s *fakePromptStore, w *RunPaidPromptsWorker) {
			s.delivery.Paid = false
			w.enabled = func(_ context.Context, flag string) bool { return flag != FlagPlanGranted }
		},
		"webhooks off": func(_ *fakePromptStore, w *RunPaidPromptsWorker) {
			w.enabled = func(_ context.Context, flag string) bool { return flag != FlagWebhookSubscriptions }
		},
		"already not relevant": func(s *fakePromptStore, _ *RunPaidPromptsWorker) {
			outcome := store.DirectedNotRelevant
			s.delivery.DirectedOutcome = &outcome
		},
	} {
		st := &fakePromptStore{delivery: directedFixture(t), agenda: store.AgendaVersion{Text: agendaText}}
		fake := llmtest.Returning(`{"relevant": true, "message": "x"}`)
		w := promptWorker(st, fake, nil)
		change(st, w)
		if err := w.Work(t.Context(), promptJob(1)); err != nil || len(fake.Calls()) != 0 || len(st.runs) != 0 {
			t.Errorf("%s: Work = %v, calls %d, runs %d", name, err, len(fake.Calls()), len(st.runs))
		}
	}
}

const agendaText = "REGULAR BOARD MEETING\n1. Approve the minutes\n2. Adopt the 2027 budget"

func TestRunPaidPromptsStoresNothingRelevantWithItsTokens(t *testing.T) {
	st := &fakePromptStore{delivery: directedFixture(t), agenda: store.AgendaVersion{Text: agendaText}}
	fake := &llmtest.Fake{Respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"relevant": false, "message": ""}`, FinishReason: "stop", Model: "anthropic/strong", Usage: llm.Usage{InputTokens: 1200, OutputTokens: 9}}, nil
	}}
	var logs bytes.Buffer

	if err := promptWorker(st, fake, &logs).Work(t.Context(), promptJob(1)); err != nil {
		t.Fatalf("Work = %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Tier != llm.TierStrong || !strings.Contains(calls[0].User, ownerFocus) || !strings.Contains(calls[0].User, "Adopt the 2027 budget") {
		t.Fatalf("calls = %+v", calls)
	}
	want := store.DirectedRunInput{
		SubscriptionID: st.delivery.SubscriptionID, EventID: st.delivery.Event.ID, Outcome: store.DirectedNotRelevant,
		Model: "anthropic/strong", Calls: 1, InputTokens: 1200, OutputTokens: 9,
	}
	if len(st.runs) != 1 || st.runs[0] != want {
		t.Errorf("runs = %+v, want %+v", st.runs, want)
	}
	if !strings.Contains(logs.String(), "nothing relevant") || strings.Contains(logs.String(), "A Line") {
		t.Errorf("logs = %s", logs.String())
	}
}

func TestRunPaidPromptsRunsForAGrantedFreeUser(t *testing.T) {
	st := &fakePromptStore{delivery: directedFixture(t), agenda: store.AgendaVersion{Text: agendaText}}
	st.delivery.Paid = false
	fake := llmtest.Returning(`{"relevant": false, "message": ""}`)
	if err := promptWorker(st, fake, nil).Work(t.Context(), promptJob(1)); err != nil {
		t.Fatalf("Work = %v", err)
	}
	if len(fake.Calls()) != 1 || len(st.runs) != 1 {
		t.Errorf("calls %d, runs %d, want the prompt run", len(fake.Calls()), len(st.runs))
	}
}

func TestRunPaidPromptsReadsTheTranscriptOfATranscriptEvent(t *testing.T) {
	d := directedFixture(t)
	d.Event.Type = store.EventTranscriptReady
	d.Event.Payload, _ = json.Marshal(store.TranscriptEventPayload{TranscriptID: uuid.New(), RecordingID: uuid.New()})
	st := &fakePromptStore{delivery: d, transcript: store.Transcript{Text: "The board voted to keep student fares at one dollar."}}
	fake := llmtest.Returning(`{"relevant": false, "message": ""}`)

	if err := promptWorker(st, fake, nil).Work(t.Context(), promptJob(1)); err != nil {
		t.Fatal(err)
	}
	if calls := fake.Calls(); len(calls) != 1 || !strings.Contains(calls[0].User, "keep student fares") || !strings.Contains(calls[0].System, "automatic transcript") {
		t.Errorf("calls = %+v", calls)
	}
}

func TestRunPaidPromptsWithoutSourceTextCallsNoModel(t *testing.T) {
	st := &fakePromptStore{delivery: directedFixture(t), agenda: store.AgendaVersion{Text: " \n "}}
	fake := llmtest.Returning("never")

	if err := promptWorker(st, fake, nil).Work(t.Context(), promptJob(1)); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls()) != 0 || len(st.runs) != 1 || st.runs[0].Outcome != store.DirectedNotRelevant || st.runs[0].Calls != 0 {
		t.Errorf("calls %d, runs %+v", len(fake.Calls()), st.runs)
	}
}

func TestRunPaidPromptsCancelsWhenTheSourceIsGone(t *testing.T) {
	for name, change := range map[string]func(*fakePromptStore){
		"agenda gone":   func(s *fakePromptStore) { s.agendaErr = store.ErrNotFound },
		"unknown event": func(s *fakePromptStore) { s.delivery.Event.Type = "meeting_moved" },
		"bad payload":   func(s *fakePromptStore) { s.delivery.Event.Payload = []byte(`{}`) },
	} {
		st := &fakePromptStore{delivery: directedFixture(t)}
		change(st)
		if err := promptWorker(st, llmtest.Returning("never"), nil).Work(t.Context(), promptJob(1)); !isCancel(err) {
			t.Errorf("%s: Work = %v, want a cancel", name, err)
		}
	}
}

func TestRunPaidPromptsRetriesAFailureAndRecordsItsTokens(t *testing.T) {
	st := &fakePromptStore{delivery: directedFixture(t), agenda: store.AgendaVersion{Text: agendaText}}
	fake := &llmtest.Fake{Respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{Text: "Student fares drop.", FinishReason: "stop", Usage: llm.Usage{InputTokens: 700, OutputTokens: 5}}, nil
	}}

	err := promptWorker(st, fake, nil).Work(t.Context(), promptJob(1))

	if err == nil || isCancel(err) || isSnooze(err) {
		t.Fatalf("Work = %v, want a retryable error", err)
	}
	if len(st.runs) != 1 || st.runs[0].Outcome != "" || st.runs[0].InputTokens != 700 || st.runs[0].Calls != 1 {
		t.Errorf("runs = %+v, want the usage recorded without an outcome", st.runs)
	}
}

func TestRunPaidPromptsWaitsForTheLLM(t *testing.T) {
	for name, client := range map[string]llm.Client{
		"no client":      nil,
		"not configured": llmtest.Failing(llm.ErrNotConfigured),
		"account":        llmtest.Failing(&llm.Error{StatusCode: http.StatusPaymentRequired, Account: true, Err: errors.New("spend limit reached")}),
	} {
		st := &fakePromptStore{delivery: directedFixture(t), agenda: store.AgendaVersion{Text: agendaText}}
		if err := promptWorker(st, client, nil).Work(t.Context(), promptJob(1)); !isSnooze(err) {
			t.Errorf("%s: Work = %v, want a snooze", name, err)
		}
		if len(st.runs) != 0 {
			t.Errorf("%s: runs = %+v", name, st.runs)
		}
	}
}

func TestDeliverWebhookCarriesTheDirectedMessage(t *testing.T) {
	sealer := testWebhookSealer(t)
	d := deliveryFixture(t, sealer)
	outcome, message := store.DirectedRelevant, "Student fares on the A Line drop to $1 in July."
	d.DirectedOutcome, d.DirectedMessage = &outcome, &message
	st := &fakeDeliveryStore{delivery: d}
	sender := &fakeSender{result: webhooks.Result{StatusCode: http.StatusOK}}

	if err := deliverWorker(t, st, sender, sealer).Work(t.Context(), deliverJob()); err != nil {
		t.Fatal(err)
	}

	var body struct {
		Message *struct {
			Text   string `json:"text"`
			Source string `json:"source"`
		} `json:"message"`
	}
	if len(sender.requests) != 1 {
		t.Fatalf("requests = %d", len(sender.requests))
	}
	if err := json.Unmarshal(sender.requests[0].Body, &body); err != nil || body.Message == nil || body.Message.Text != message || body.Message.Source != "directed_prompt" {
		t.Errorf("body = %s, %v", sender.requests[0].Body, err)
	}
}

func TestDeliverWebhookSendsNothingWhenNothingWasRelevant(t *testing.T) {
	sealer := testWebhookSealer(t)
	d := deliveryFixture(t, sealer)
	outcome := store.DirectedNotRelevant
	d.DirectedOutcome = &outcome
	st := &fakeDeliveryStore{delivery: d}
	sender := &fakeSender{}

	if err := deliverWorker(t, st, sender, sealer).Work(t.Context(), deliverJob()); err != nil || len(sender.requests) != 0 || len(st.attempts) != 0 {
		t.Errorf("Work = %v, requests %d, attempts %d", err, len(sender.requests), len(st.attempts))
	}
}

func TestDeliverWebhookAfterAFailedPromptIsStandard(t *testing.T) {
	sealer := testWebhookSealer(t)
	d := deliveryFixture(t, sealer)
	outcome := store.DirectedFailed
	d.DirectedOutcome = &outcome
	st := &fakeDeliveryStore{delivery: d}
	sender := &fakeSender{result: webhooks.Result{StatusCode: http.StatusOK}}

	if err := deliverWorker(t, st, sender, sealer).Work(t.Context(), deliverJob()); err != nil || len(sender.requests) != 1 {
		t.Fatalf("Work = %v, requests %d", err, len(sender.requests))
	}
	if bytes.Contains(sender.requests[0].Body, []byte(`"message"`)) {
		t.Errorf("body = %s", sender.requests[0].Body)
	}
}
