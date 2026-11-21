package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const (
	FlagWebhookSubscriptions  = "webhook-subscriptions"
	flagAccounts              = "accounts"
	WebhookQueue              = "webhooks"
	webhookQueueWorkers       = 10
	webhookSweepInterval      = time.Minute
	webhookSweepLimit         = 100
	webhookSweepLookback      = 1000
	webhookFanOutAttempts     = 5
	webhookDeliverAttempts    = 5
	webhookDeliverTimeout     = webhooks.DefaultRequestTimeout + 15*time.Second
	webhookUnconfiguredSnooze = 10 * time.Minute
)

type Webhooks struct {
	Sealer       *webhooks.Sealer
	Sender       WebhookSender
	PublicWebURL string
}

type WebhookSender interface {
	Send(ctx context.Context, req webhooks.Request) webhooks.Result
}

func (w Webhooks) configured() bool { return w.Sealer.Configured() && w.Sender != nil }

func webhooksAllowed(ctx context.Context, enabled func(context.Context, string) bool, workOSUserID string) bool {
	ctx = featureflags.WithIdentity(ctx, workOSUserID)
	return enabled(ctx, flagAccounts) && enabled(ctx, FlagWebhookSubscriptions)
}

type EnqueueWebhookFanOutsArgs struct {
	Slot time.Time `json:"slot"`
}

func (EnqueueWebhookFanOutsArgs) Kind() string { return "enqueue_webhook_fan_outs" }

func (EnqueueWebhookFanOutsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 1,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type webhookSweepStore interface {
	ListEventsAwaitingFanOut(ctx context.Context, p store.FanOutParams) ([]uuid.UUID, error)
}

type EnqueueWebhookFanOutsWorker struct {
	river.WorkerDefaults[EnqueueWebhookFanOutsArgs]

	logger   *slog.Logger
	store    webhookSweepStore
	webhooks Webhooks
	queue    string
}

func (w *EnqueueWebhookFanOutsWorker) Work(ctx context.Context, job *river.Job[EnqueueWebhookFanOutsArgs]) error {
	logger := w.logger.With("job_id", job.ID, "slot", job.Args.Slot)
	if !w.webhooks.configured() {
		logger.DebugContext(ctx, "webhook events not swept: WEBHOOK_SECRET_KEY is not configured")
		return nil
	}
	ids, err := w.store.ListEventsAwaitingFanOut(ctx, store.FanOutParams{Limit: webhookSweepLimit, Lookback: webhookSweepLookback})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		logger.DebugContext(ctx, "webhook events swept: none waiting")
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	params := make([]river.InsertManyParams, 0, len(ids))
	for _, id := range ids {
		params = append(params, river.InsertManyParams{
			Args:       FanOutWebhooksArgs{EventID: id},
			InsertOpts: &river.InsertOpts{Queue: w.queue},
		})
	}
	results, err := client.InsertMany(ctx, params)
	if err != nil {
		return fmt.Errorf("enqueue webhook fan-outs: %w", err)
	}
	skipped := 0
	for _, r := range results {
		if r.UniqueSkippedAsDuplicate {
			skipped++
		}
	}
	logger.InfoContext(ctx, "webhook events swept", "waiting", len(ids), "inserted", len(results)-skipped, "duplicates", skipped,
		"limit", webhookSweepLimit)
	return nil
}

func periodicWebhookSweep(clock sources.Clock, queue string) river.PeriodicJobConstructor {
	return func() (river.JobArgs, *river.InsertOpts) {
		return EnqueueWebhookFanOutsArgs{Slot: clock.Now().UTC().Truncate(webhookSweepInterval)}, &river.InsertOpts{Queue: queue}
	}
}

type FanOutWebhooksArgs struct {
	EventID uuid.UUID `json:"event_id"`
}

func (FanOutWebhooksArgs) Kind() string { return "fan_out_webhooks" }

func (FanOutWebhooksArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: webhookFanOutAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type webhookFanOutStore interface {
	WebhookFanOutTargets(ctx context.Context, eventID uuid.UUID) (store.WebhookFanOut, error)
	ClaimWebhookFanOutTx(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, sequence int64, deliveries int) (bool, error)
}

type FanOutWebhooksWorker struct {
	river.WorkerDefaults[FanOutWebhooksArgs]

	logger       *slog.Logger
	pool         *pgxpool.Pool
	store        webhookFanOutStore
	enabled      func(context.Context, string) bool
	deliverQueue string
}

func (w *FanOutWebhooksWorker) Work(ctx context.Context, job *river.Job[FanOutWebhooksArgs]) error {
	logger := w.logger.With("job_id", job.ID, "event_id", job.Args.EventID)
	fo, err := w.store.WebhookFanOutTargets(ctx, job.Args.EventID)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(fmt.Errorf("fan out webhooks: event %s: %w", job.Args.EventID, err))
	}
	if err != nil {
		return err
	}
	allowed := make(map[string]bool)
	var targets []store.WebhookTarget
	for _, t := range fo.Targets {
		on, seen := allowed[t.WorkOSUserID]
		if !seen {
			on = webhooksAllowed(ctx, w.enabled, t.WorkOSUserID)
			allowed[t.WorkOSUserID] = on
		}
		if on {
			targets = append(targets, t)
		}
	}
	claimed := false
	err = pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		var err error
		if claimed, err = w.store.ClaimWebhookFanOutTx(ctx, tx, fo.EventID, fo.Sequence, len(targets)); err != nil || !claimed || len(targets) == 0 {
			return err
		}
		client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
		if err != nil {
			return err
		}
		params := make([]river.InsertManyParams, 0, len(targets))
		for _, t := range targets {
			params = append(params, river.InsertManyParams{
				Args:       DeliverWebhookArgs{SubscriptionID: t.SubscriptionID, EventID: fo.EventID},
				InsertOpts: &river.InsertOpts{Queue: w.deliverQueue},
			})
		}
		if _, err := client.InsertManyTx(ctx, tx, params); err != nil {
			return fmt.Errorf("enqueue webhook deliveries: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !claimed {
		logger.InfoContext(ctx, "webhook event already fanned out")
		return nil
	}
	logger.InfoContext(ctx, "webhook event fanned out", "subscriptions", len(fo.Targets), "deliveries", len(targets))
	return nil
}

type DeliverWebhookArgs struct {
	SubscriptionID uuid.UUID `json:"subscription_id"`
	EventID        uuid.UUID `json:"event_id"`
}

func (DeliverWebhookArgs) Kind() string { return "deliver_webhook" }

func (DeliverWebhookArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       WebhookQueue,
		MaxAttempts: webhookDeliverAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type webhookDeliveryStore interface {
	WebhookDelivery(ctx context.Context, subscriptionID, eventID uuid.UUID) (store.WebhookDelivery, error)
	RecordWebhookAttempt(ctx context.Context, in store.WebhookAttemptInput) error
}

type DeliverWebhookWorker struct {
	river.WorkerDefaults[DeliverWebhookArgs]

	logger   *slog.Logger
	store    webhookDeliveryStore
	webhooks Webhooks
	clock    sources.Clock
	enabled  func(context.Context, string) bool
}

func (w *DeliverWebhookWorker) Timeout(*river.Job[DeliverWebhookArgs]) time.Duration {
	return webhookDeliverTimeout
}

func (w *DeliverWebhookWorker) Work(ctx context.Context, job *river.Job[DeliverWebhookArgs]) error {
	logger := w.logger.With("job_id", job.ID, "subscription_id", job.Args.SubscriptionID, "event_id", job.Args.EventID, "attempt", job.Attempt)
	d, err := w.store.WebhookDelivery(ctx, job.Args.SubscriptionID, job.Args.EventID)
	if errors.Is(err, store.ErrNotFound) {
		logger.InfoContext(ctx, "webhook not delivered", "reason", "subscription_gone")
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case d.Status != store.WebhookActive:
		logger.InfoContext(ctx, "webhook not delivered", "reason", "paused")
		return nil
	case !d.Paid:
		logger.InfoContext(ctx, "webhook not delivered", "reason", "free_plan", "user_id", d.UserID)
		return nil
	case !webhooksAllowed(ctx, w.enabled, d.WorkOSUserID):
		logger.InfoContext(ctx, "webhook not delivered", "reason", "flag_off", "user_id", d.UserID)
		return nil
	case !w.webhooks.configured():
		logger.WarnContext(ctx, "webhook not delivered yet: WEBHOOK_SECRET_KEY is not configured on the worker")
		return river.JobSnooze(webhookUnconfiguredSnooze)
	}
	body, err := webhooks.Body(d.Format, d.PayloadVersion, webhooks.Event{
		ID:         d.Event.ID,
		Type:       d.Event.Type,
		Payload:    d.Event.Payload,
		OccurredAt: d.Event.OccurredAt,
		AgencySlug: d.AgencySlug,
		AgencyName: d.AgencyName,
		Meeting:    d.Meeting,
		PageURL:    webhooks.MeetingPage(w.webhooks.PublicWebURL, d.Meeting),
	})
	if err != nil {
		return river.JobCancel(err)
	}
	started := w.clock.Now()
	secret, err := w.webhooks.Sealer.Open(d.SubscriptionID, d.SecretCiphertext)
	var result webhooks.Result
	if err != nil {
		result = webhooks.Result{Err: errors.New("the signing secret cannot be read; rotate the secret")}
	} else {
		result = w.webhooks.Sender.Send(ctx, webhooks.Request{
			URL:    d.URL,
			ID:     d.Event.ID.String(),
			Body:   body,
			Secret: secret,
			At:     started,
		})
	}
	recordErr := w.store.RecordWebhookAttempt(context.WithoutCancel(ctx), store.WebhookAttemptInput{
		SubscriptionID: d.SubscriptionID,
		EventID:        d.Event.ID,
		Attempt:        job.Attempt,
		StartedAt:      started,
		Duration:       result.Duration,
		StatusCode:     result.StatusCode,
		Error:          result.Problem(),
		Succeeded:      result.Succeeded(),
	})
	if recordErr != nil {
		logger.ErrorContext(ctx, "webhook attempt not recorded", "error", recordErr)
	}
	logger = logger.With("event_type", string(d.Event.Type), "format", string(d.Format), "status_code", result.StatusCode, "duration_ms", result.Duration.Milliseconds())
	switch {
	case result.Succeeded():
		logger.InfoContext(ctx, "webhook delivered")
		return nil
	case err != nil, result.Refused():
		logger.WarnContext(ctx, "webhook delivery cancelled", "problem", result.Problem())
		return river.JobCancel(errors.New(result.Problem()))
	default:
		logger.WarnContext(ctx, "webhook delivery failed", "problem", result.Problem(), "max_attempts", job.MaxAttempts)
		return errors.New(result.Problem())
	}
}
