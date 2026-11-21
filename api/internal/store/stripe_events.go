package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrInvalidStripeEvent = errors.New("invalid stripe event")

var terminalSubscriptionStatuses = []string{"canceled", "incomplete_expired"}

type StripeEvent struct {
	ID        string
	Type      string
	CreatedAt time.Time
}

type SubscriptionState struct {
	ID         string
	CustomerID string
	Status     string
	Paid       bool
}

type StripeEventOutcome string

const (
	StripeEventApplied         StripeEventOutcome = "applied"
	StripeEventDuplicate       StripeEventOutcome = "duplicate"
	StripeEventStale           StripeEventOutcome = "stale"
	StripeEventUnknownCustomer StripeEventOutcome = "unknown_customer"
	StripeEventIgnored         StripeEventOutcome = "ignored"
)

type StripeEventResult struct {
	Outcome           StripeEventOutcome
	UserID            uuid.UUID
	PreviousPlan      Plan
	Plan              Plan
	PaidSubscriptions int
}

func (s *Store) ApplyStripeEvent(ctx context.Context, event StripeEvent, sub *SubscriptionState) (StripeEventResult, error) {
	if err := validStripeEvent(event, sub); err != nil {
		return StripeEventResult{}, fmt.Errorf("apply stripe event: %w", err)
	}
	var result StripeEventResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		result, err = applyStripeEvent(ctx, tx, event, sub)
		return err
	})
	if err != nil {
		return StripeEventResult{}, fmt.Errorf("apply stripe event %s: %w", event.ID, err)
	}
	return result, nil
}

func validStripeEvent(event StripeEvent, sub *SubscriptionState) error {
	switch {
	case event.ID == "" || event.Type == "" || event.CreatedAt.IsZero():
		return fmt.Errorf("%w: id, type and creation time are required", ErrInvalidStripeEvent)
	case sub != nil && (sub.ID == "" || sub.CustomerID == "" || sub.Status == ""):
		return fmt.Errorf("%w: the subscription needs an id, a customer and a status", ErrInvalidStripeEvent)
	}
	return nil
}

func applyStripeEvent(ctx context.Context, tx pgx.Tx, event StripeEvent, sub *SubscriptionState) (StripeEventResult, error) {
	tag, err := tx.Exec(ctx, `
INSERT INTO stripe_events (id, type, created_at) VALUES ($1, $2, $3)
ON CONFLICT (id) DO NOTHING`, event.ID, event.Type, event.CreatedAt)
	if err != nil {
		return StripeEventResult{}, err
	}
	if tag.RowsAffected() == 0 {
		return StripeEventResult{Outcome: StripeEventDuplicate}, nil
	}
	if sub == nil {
		return StripeEventResult{Outcome: StripeEventIgnored}, nil
	}

	var result StripeEventResult
	err = tx.QueryRow(ctx, `SELECT id, plan FROM users WHERE stripe_customer_id = $1 FOR UPDATE`, sub.CustomerID).Scan(&result.UserID, &result.PreviousPlan)
	if errors.Is(err, pgx.ErrNoRows) {
		return StripeEventResult{Outcome: StripeEventUnknownCustomer}, nil
	}
	if err != nil {
		return StripeEventResult{}, err
	}

	tag, err = tx.Exec(ctx, `
INSERT INTO stripe_subscriptions (id, user_id, customer_id, status, paid, event_id, event_created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE
SET user_id = EXCLUDED.user_id,
    customer_id = EXCLUDED.customer_id,
    status = EXCLUDED.status,
    paid = EXCLUDED.paid,
    event_id = EXCLUDED.event_id,
    event_created_at = EXCLUDED.event_created_at,
    updated_at = now()
WHERE stripe_subscriptions.event_created_at <= EXCLUDED.event_created_at
  AND stripe_subscriptions.status <> ALL($8)`,
		sub.ID, result.UserID, sub.CustomerID, sub.Status, sub.Paid, event.ID, event.CreatedAt, terminalSubscriptionStatuses)
	if err != nil {
		return StripeEventResult{}, err
	}
	result.Outcome = StripeEventApplied
	if tag.RowsAffected() == 0 {
		result.Outcome = StripeEventStale
	}

	err = tx.QueryRow(ctx, `
WITH counted AS (
    SELECT count(*) AS paid
    FROM stripe_subscriptions s
    JOIN users u ON u.id = s.user_id AND u.stripe_customer_id = s.customer_id
    WHERE s.user_id = $1 AND s.paid
), target AS (
    SELECT CASE WHEN paid > 0 THEN 'paid' ELSE 'free' END AS plan, paid FROM counted
)
UPDATE users
SET plan = target.plan,
    updated_at = CASE WHEN users.plan = target.plan THEN users.updated_at ELSE now() END
FROM target
WHERE users.id = $1
RETURNING users.plan, target.paid`, result.UserID).Scan(&result.Plan, &result.PaidSubscriptions)
	if err != nil {
		return StripeEventResult{}, err
	}
	return result, nil
}
