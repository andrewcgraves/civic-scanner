package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	SignatureTolerance = webhook.DefaultTolerance

	EventCheckoutCompleted          = "checkout.session.completed"
	EventCheckoutAsyncPaymentPassed = "checkout.session.async_payment_succeeded"
	EventSubscriptionCreated        = "customer.subscription.created"
	EventSubscriptionUpdated        = "customer.subscription.updated"
	EventSubscriptionDeleted        = "customer.subscription.deleted"
	EventSubscriptionPaused         = "customer.subscription.paused"
	EventSubscriptionResumed        = "customer.subscription.resumed"

	CheckoutCompletedStatus = "checkout_completed"
)

var (
	ErrInvalidSignature = errors.New("the stripe signature is not valid")
	ErrInvalidEvent     = errors.New("the stripe event is not valid")
)

var paidStatuses = map[string]bool{
	string(stripe.SubscriptionStatusActive):   true,
	string(stripe.SubscriptionStatusTrialing): true,
	string(stripe.SubscriptionStatusPastDue):  true,
}

var subscriptionEvents = map[string]bool{
	EventSubscriptionCreated: true,
	EventSubscriptionUpdated: true,
	EventSubscriptionDeleted: true,
	EventSubscriptionPaused:  true,
	EventSubscriptionResumed: true,
}

var checkoutEvents = map[string]bool{
	EventCheckoutCompleted:          true,
	EventCheckoutAsyncPaymentPassed: true,
}

var signatureErrors = []error{
	stripe.ErrWebhookInvalidHeader,
	stripe.ErrWebhookNoValidSignature,
	stripe.ErrWebhookNotSigned,
	stripe.ErrWebhookTooOld,
	stripe.ErrWebhookEmptySecret,
}

func PaidStatus(status string) bool { return paidStatuses[status] }

type EventStore interface {
	ApplyStripeEvent(ctx context.Context, event store.StripeEvent, sub *store.SubscriptionState) (store.StripeEventResult, error)
}

type Webhook struct {
	secret config.Secret
	store  EventStore
	logger *slog.Logger
}

type Receipt struct {
	EventID   string
	EventType string
	Outcome   store.StripeEventOutcome
}

func NewWebhook(cfg config.Stripe, events EventStore, opts ...Option) *Webhook {
	s := settings{logger: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(&s)
	}
	w := &Webhook{store: events, logger: s.logger}
	if cfg.WebhookConfigured() {
		w.secret = cfg.WebhookSecret
	}
	return w
}

func (w *Webhook) Configured() bool { return w != nil && w.secret.IsSet() && w.store != nil }

func (w *Webhook) Receive(ctx context.Context, payload []byte, signature string) (Receipt, error) {
	if !w.Configured() {
		return Receipt{}, ErrNotConfigured
	}
	event, err := webhook.ConstructEventWithOptions(payload, signature, w.secret.Reveal(), webhook.ConstructEventOptions{
		Tolerance:                SignatureTolerance,
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		for _, sigErr := range signatureErrors {
			if errors.Is(err, sigErr) {
				return Receipt{}, fmt.Errorf("%w: %w", ErrInvalidSignature, sigErr)
			}
		}
		return Receipt{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	receipt := Receipt{EventID: event.ID, EventType: string(event.Type), Outcome: store.StripeEventIgnored}
	if event.ID == "" || event.Type == "" || event.Created <= 0 {
		return receipt, fmt.Errorf("%w: the event has no id, type or creation time", ErrInvalidEvent)
	}
	handled := subscriptionEvents[receipt.EventType] || checkoutEvents[receipt.EventType]
	if !handled || event.Account != "" {
		w.logger.DebugContext(ctx, "stripe event ignored", "event_id", event.ID, "event_type", receipt.EventType, "account", event.Account)
		return receipt, nil
	}

	sub, err := subscriptionState(event)
	if err != nil {
		w.logger.ErrorContext(ctx, "stripe event could not be read", "event_id", event.ID, "event_type", receipt.EventType,
			"api_version", event.APIVersion, "error", err)
		return receipt, fmt.Errorf("%w: %s: %w", ErrInvalidEvent, event.Type, err)
	}
	result, err := w.store.ApplyStripeEvent(ctx, store.StripeEvent{
		ID:        event.ID,
		Type:      receipt.EventType,
		CreatedAt: time.Unix(event.Created, 0).UTC(),
	}, sub)
	if err != nil {
		return receipt, fmt.Errorf("stripe event %s: %w", event.ID, err)
	}
	receipt.Outcome = result.Outcome
	w.logResult(ctx, event, sub, result)
	return receipt, nil
}

func (w *Webhook) logResult(ctx context.Context, event stripe.Event, sub *store.SubscriptionState, result store.StripeEventResult) {
	attrs := []any{
		"event_id", event.ID, "event_type", string(event.Type), "outcome", string(result.Outcome),
		"api_version", event.APIVersion, "livemode", event.Livemode,
	}
	if sub != nil {
		attrs = append(attrs, "subscription", sub.ID, "customer", sub.CustomerID, "status", sub.Status)
	}
	if result.UserID != uuid.Nil {
		attrs = append(attrs, "user_id", result.UserID.String(), "plan", string(result.Plan), "previous_plan", string(result.PreviousPlan))
	}
	switch {
	case result.Outcome == store.StripeEventUnknownCustomer:
		w.logger.WarnContext(ctx, "stripe event for a customer no user has; ignored", attrs...)
	case result.PaidSubscriptions > 1:
		w.logger.WarnContext(ctx, "user has more than one paid subscription; refund or cancel the extra one in Stripe",
			append(attrs, "paid_subscriptions", result.PaidSubscriptions)...)
	default:
		w.logger.InfoContext(ctx, "stripe event processed", attrs...)
	}
}

type stripeRef string

func (r *stripeRef) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*r = ""
		return nil
	}
	var id string
	if err := json.Unmarshal(data, &id); err == nil {
		*r = stripeRef(id)
		return nil
	}
	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*r = stripeRef(object.ID)
	return nil
}

type subscriptionObject struct {
	Object   string    `json:"object"`
	ID       string    `json:"id"`
	Customer stripeRef `json:"customer"`
	Status   string    `json:"status"`
}

type checkoutObject struct {
	Object        string    `json:"object"`
	ID            string    `json:"id"`
	Mode          string    `json:"mode"`
	PaymentStatus string    `json:"payment_status"`
	Customer      stripeRef `json:"customer"`
	Subscription  stripeRef `json:"subscription"`
}

func subscriptionState(event stripe.Event) (*store.SubscriptionState, error) {
	if event.Data == nil || len(event.Data.Raw) == 0 {
		return nil, errors.New("the event has no data object")
	}
	if subscriptionEvents[string(event.Type)] {
		var sub subscriptionObject
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			return nil, err
		}
		if sub.Object != "subscription" || sub.ID == "" || sub.Customer == "" || sub.Status == "" {
			return nil, errors.New("the data object is not a subscription with an id, a customer and a status")
		}
		return &store.SubscriptionState{ID: sub.ID, CustomerID: string(sub.Customer), Status: sub.Status, Paid: PaidStatus(sub.Status)}, nil
	}

	var session checkoutObject
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		return nil, err
	}
	if session.Object != "checkout.session" || session.ID == "" {
		return nil, errors.New("the data object is not a checkout session with an id")
	}
	paid := session.PaymentStatus == string(stripe.CheckoutSessionPaymentStatusPaid) ||
		session.PaymentStatus == string(stripe.CheckoutSessionPaymentStatusNoPaymentRequired)
	if session.Mode != string(stripe.CheckoutSessionModeSubscription) || session.Subscription == "" || session.Customer == "" || !paid {
		return nil, nil
	}
	return &store.SubscriptionState{ID: string(session.Subscription), CustomerID: string(session.Customer), Status: CheckoutCompletedStatus, Paid: true}, nil
}
