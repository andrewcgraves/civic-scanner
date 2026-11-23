package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/summarize"
)

const (
	FlagDirectedPrompts        = "webhook-directed-prompts"
	directedPromptAttempts     = 5
	directedUnconfiguredSnooze = 10 * time.Minute
)

var errUnsupportedDirectedEvent = errors.New("event has no text to read")

func directedAllowed(ctx context.Context, enabled func(context.Context, string) bool, workOSUserID string) bool {
	return webhooksAllowed(ctx, enabled, workOSUserID) && enabled(featureflags.WithIdentity(ctx, workOSUserID), FlagDirectedPrompts)
}

type RunPaidPromptsArgs struct {
	SubscriptionID uuid.UUID `json:"subscription_id"`
	EventID        uuid.UUID `json:"event_id"`
}

func (RunPaidPromptsArgs) Kind() string { return "run_paid_prompts" }

func (RunPaidPromptsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: directedPromptAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type paidPromptStore interface {
	WebhookDelivery(ctx context.Context, subscriptionID, eventID uuid.UUID) (store.WebhookDelivery, error)
	GetAgendaVersion(ctx context.Context, versionID uuid.UUID) (store.AgendaVersion, error)
	GetTranscriptByID(ctx context.Context, transcriptID uuid.UUID) (store.Transcript, error)
	RecordDirectedRun(ctx context.Context, in store.DirectedRunInput) (store.DirectedRun, error)
	RecordDirectedRunTx(ctx context.Context, tx pgx.Tx, in store.DirectedRunInput) (store.DirectedRun, error)
}

type RunPaidPromptsWorker struct {
	river.WorkerDefaults[RunPaidPromptsArgs]

	logger       *slog.Logger
	pool         *pgxpool.Pool
	store        paidPromptStore
	llm          llm.Client
	llmTimeout   time.Duration
	enabled      func(context.Context, string) bool
	deliverQueue string
}

func (w *RunPaidPromptsWorker) Timeout(*river.Job[RunPaidPromptsArgs]) time.Duration {
	return w.llmTimeout + summarizeTimeoutMargin
}

func (w *RunPaidPromptsWorker) Work(ctx context.Context, job *river.Job[RunPaidPromptsArgs]) error {
	logger := w.logger.With("job_id", job.ID, "subscription_id", job.Args.SubscriptionID, "event_id", job.Args.EventID, "attempt", job.Attempt)
	d, err := w.store.WebhookDelivery(ctx, job.Args.SubscriptionID, job.Args.EventID)
	if errors.Is(err, store.ErrNotFound) {
		logger.InfoContext(ctx, "directed prompt not run", "reason", "subscription_gone")
		return nil
	}
	if err != nil {
		return err
	}
	logger = logger.With("user_id", d.UserID, "event_type", string(d.Event.Type))
	switch {
	case d.Status != store.WebhookActive:
		logger.InfoContext(ctx, "directed prompt not run", "reason", "paused")
		return nil
	case !planAllows(ctx, w.enabled, d.WorkOSUserID, d.Paid):
		logger.InfoContext(ctx, "directed prompt not run", "reason", "free_plan")
		return nil
	case !webhooksAllowed(ctx, w.enabled, d.WorkOSUserID):
		logger.InfoContext(ctx, "directed prompt not run", "reason", "flag_off")
		return nil
	case d.Prompt == nil:
		logger.InfoContext(ctx, "directed prompt not run: standard delivery", "reason", "no_prompt")
		return w.deliver(ctx, nil, d)
	case !directedAllowed(ctx, w.enabled, d.WorkOSUserID):
		logger.InfoContext(ctx, "directed prompt not run: standard delivery", "reason", "directed_flag_off", "flag", FlagDirectedPrompts)
		return w.deliver(ctx, nil, d)
	case d.DirectedOutcome != nil && *d.DirectedOutcome == store.DirectedRelevant:
		logger.InfoContext(ctx, "directed prompt already run", "outcome", string(*d.DirectedOutcome))
		return w.deliver(ctx, nil, d)
	case d.DirectedOutcome != nil && d.DirectedOutcome.Final():
		logger.InfoContext(ctx, "directed prompt already run", "outcome", string(*d.DirectedOutcome))
		return nil
	case w.llm == nil:
		logger.WarnContext(ctx, "directed prompt not run yet: no LLM configured on the worker")
		return river.JobSnooze(directedUnconfiguredSnooze)
	}

	source, text, err := w.sourceText(ctx, d.Event)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, errUnsupportedDirectedEvent) {
		return river.JobCancel(fmt.Errorf("run paid prompts: %w", err))
	}
	if err != nil {
		return err
	}
	run := store.DirectedRunInput{SubscriptionID: d.SubscriptionID, EventID: d.Event.ID}
	if strings.TrimSpace(text) == "" {
		run.Outcome = store.DirectedNotRelevant
		if _, err := w.store.RecordDirectedRun(ctx, run); err != nil {
			return err
		}
		logger.InfoContext(ctx, "directed prompt: nothing relevant, not delivered", "reason", "empty_source")
		return nil
	}

	result, err := summarize.Directed(ctx, w.llm, llm.TierStrong, source, *d.Prompt, text)
	run.Model = result.Response.Model
	run.InputTokens = result.Response.Usage.InputTokens
	run.OutputTokens = result.Response.Usage.OutputTokens
	if result.Input.Delimiter != "" {
		run.Calls = 1
	}
	attrs := []any{
		"tier", llm.TierStrong, "model", result.Response.Model, "finish_reason", result.Response.FinishReason,
		"input_tokens", run.InputTokens, "output_tokens", run.OutputTokens,
		"input_bytes", result.Input.Bytes, "text_bytes", len(text), "input_truncated", result.Input.Truncated,
	}
	if errors.Is(err, llm.ErrNotConfigured) {
		logger.WarnContext(ctx, "directed prompt not run yet: LLM not configured", attrs...)
		return river.JobSnooze(directedUnconfiguredSnooze)
	}
	if snooze := llmAccountSnoozeFor(ctx, logger.With("tier", llm.TierStrong), err); snooze != nil {
		return snooze
	}
	if err != nil {
		return w.failed(ctx, logger, job, d, run, err, attrs)
	}

	if !result.Relevant {
		run.Outcome = store.DirectedNotRelevant
		if _, err := w.store.RecordDirectedRun(context.WithoutCancel(ctx), run); err != nil {
			return err
		}
		logger.InfoContext(ctx, "directed prompt: nothing relevant, not delivered", attrs...)
		return nil
	}
	run.Outcome = store.DirectedRelevant
	run.Message = result.Message
	err = w.recordAndDeliver(context.WithoutCancel(ctx), d, run)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "directed prompt: relevant, delivery enqueued", append(attrs, "message_bytes", len(result.Message))...)
	return nil
}

func (w *RunPaidPromptsWorker) failed(ctx context.Context, logger *slog.Logger, job *river.Job[RunPaidPromptsArgs], d store.WebhookDelivery, run store.DirectedRunInput, err error, attrs []any) error {
	attrs = append(attrs, "error", err)
	final := !llm.IsRetryable(err) || job.Attempt >= job.MaxAttempts
	if !final {
		if _, recordErr := w.store.RecordDirectedRun(context.WithoutCancel(ctx), run); recordErr != nil {
			logger.ErrorContext(ctx, "directed prompt usage not recorded", "error", recordErr)
		}
		logger.WarnContext(ctx, "directed prompt failed; will retry", attrs...)
		return fmt.Errorf("run paid prompts: %w", err)
	}
	run.Outcome = store.DirectedFailed
	if txErr := w.recordAndDeliver(context.WithoutCancel(ctx), d, run); txErr != nil {
		return txErr
	}
	logger.WarnContext(ctx, "directed prompt failed: standard delivery enqueued", attrs...)
	return river.JobCancel(fmt.Errorf("run paid prompts: %w", err))
}

func (w *RunPaidPromptsWorker) recordAndDeliver(ctx context.Context, d store.WebhookDelivery, run store.DirectedRunInput) error {
	return pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		if _, err := w.store.RecordDirectedRunTx(ctx, tx, run); err != nil {
			return err
		}
		return w.deliver(ctx, tx, d)
	})
}

func (w *RunPaidPromptsWorker) sourceText(ctx context.Context, e store.MeetingEvent) (summarize.DirectedSource, string, error) {
	switch e.Type {
	case store.EventAgendaPublished, store.EventAgendaChanged:
		var p store.AgendaEventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.AgendaVersionID == uuid.Nil {
			return "", "", fmt.Errorf("%w: %s payload of event %s", errUnsupportedDirectedEvent, e.Type, e.ID)
		}
		v, err := w.store.GetAgendaVersion(ctx, p.AgendaVersionID)
		return summarize.DirectedAgenda, v.Text, err
	case store.EventTranscriptReady:
		var p store.TranscriptEventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.TranscriptID == uuid.Nil {
			return "", "", fmt.Errorf("%w: %s payload of event %s", errUnsupportedDirectedEvent, e.Type, e.ID)
		}
		t, err := w.store.GetTranscriptByID(ctx, p.TranscriptID)
		return summarize.DirectedTranscript, t.Text, err
	}
	return "", "", fmt.Errorf("%w: type %q", errUnsupportedDirectedEvent, e.Type)
}

func (w *RunPaidPromptsWorker) deliver(ctx context.Context, tx pgx.Tx, d store.WebhookDelivery) error {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	args := DeliverWebhookArgs{SubscriptionID: d.SubscriptionID, EventID: d.Event.ID}
	opts := &river.InsertOpts{Queue: w.deliverQueue}
	if tx != nil {
		_, err = client.InsertTx(ctx, tx, args, opts)
	} else {
		_, err = client.Insert(ctx, args, opts)
	}
	if err != nil {
		return fmt.Errorf("enqueue webhook delivery: %w", err)
	}
	return nil
}
