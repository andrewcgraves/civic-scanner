package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const MaxDirectedMessageBytes = 4000

type DirectedOutcome string

const (
	DirectedRelevant    DirectedOutcome = "relevant"
	DirectedNotRelevant DirectedOutcome = "not_relevant"
	DirectedFailed      DirectedOutcome = "failed"
)

func (o DirectedOutcome) Final() bool { return o == DirectedRelevant || o == DirectedNotRelevant }

var ErrInvalidDirectedRun = errors.New("invalid directed prompt run")

type DirectedRunInput struct {
	SubscriptionID uuid.UUID
	EventID        uuid.UUID
	Outcome        DirectedOutcome
	Message        string
	Model          string
	Calls          int
	InputTokens    int
	OutputTokens   int
}

type DirectedRun struct {
	SubscriptionID uuid.UUID
	EventID        uuid.UUID
	Outcome        *DirectedOutcome
	Message        *string
	Model          string
	Calls          int
	InputTokens    int
	OutputTokens   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (s *Store) RecordDirectedRun(ctx context.Context, in DirectedRunInput) (DirectedRun, error) {
	return recordDirectedRun(ctx, s.pool, in)
}

func (s *Store) RecordDirectedRunTx(ctx context.Context, tx pgx.Tx, in DirectedRunInput) (DirectedRun, error) {
	return recordDirectedRun(ctx, tx, in)
}

const directedRunColumns = `subscription_id, event_id, outcome, message, model, calls, input_tokens, output_tokens, created_at, updated_at`

func scanDirectedRun(row pgx.Row) (DirectedRun, error) {
	var r DirectedRun
	err := row.Scan(&r.SubscriptionID, &r.EventID, &r.Outcome, &r.Message, &r.Model, &r.Calls, &r.InputTokens, &r.OutputTokens, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func recordDirectedRun(ctx context.Context, q querier, in DirectedRunInput) (DirectedRun, error) {
	var (
		outcome *DirectedOutcome
		message *string
	)
	switch in.Outcome {
	case "":
	case DirectedRelevant:
		text := truncateUTF8(in.Message, MaxDirectedMessageBytes)
		if text == "" {
			return DirectedRun{}, fmt.Errorf("record directed prompt run: relevant without a message: %w", ErrInvalidDirectedRun)
		}
		outcome, message = &in.Outcome, &text
	case DirectedNotRelevant, DirectedFailed:
		outcome = &in.Outcome
	default:
		return DirectedRun{}, fmt.Errorf("record directed prompt run: outcome %q: %w", in.Outcome, ErrInvalidDirectedRun)
	}
	if in.SubscriptionID == uuid.Nil || in.EventID == uuid.Nil || in.Calls < 0 || in.InputTokens < 0 || in.OutputTokens < 0 {
		return DirectedRun{}, fmt.Errorf("record directed prompt run: %w", ErrInvalidDirectedRun)
	}
	r, err := scanDirectedRun(q.QueryRow(ctx, `
INSERT INTO webhook_directed_prompt_runs AS r (subscription_id, event_id, outcome, message, model, calls, input_tokens, output_tokens)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (subscription_id, event_id) DO UPDATE SET
    outcome = CASE WHEN r.outcome IN ('relevant', 'not_relevant') OR EXCLUDED.outcome IS NULL THEN r.outcome ELSE EXCLUDED.outcome END,
    message = CASE WHEN r.outcome IN ('relevant', 'not_relevant') OR EXCLUDED.outcome IS NULL THEN r.message ELSE EXCLUDED.message END,
    model = CASE WHEN EXCLUDED.model = '' THEN r.model ELSE EXCLUDED.model END,
    calls = r.calls + EXCLUDED.calls,
    input_tokens = r.input_tokens + EXCLUDED.input_tokens,
    output_tokens = r.output_tokens + EXCLUDED.output_tokens,
    updated_at = now()
RETURNING `+directedRunColumns,
		in.SubscriptionID, in.EventID, outcome, message, in.Model, in.Calls, in.InputTokens, in.OutputTokens))
	if err != nil {
		return DirectedRun{}, fmt.Errorf("record directed prompt run: %w", err)
	}
	return r, nil
}

func (s *Store) GetDirectedRun(ctx context.Context, subscriptionID, eventID uuid.UUID) (DirectedRun, error) {
	r, err := scanDirectedRun(s.pool.QueryRow(ctx, `SELECT `+directedRunColumns+` FROM webhook_directed_prompt_runs
WHERE subscription_id = $1 AND event_id = $2`, subscriptionID, eventID))
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectedRun{}, ErrNotFound
	}
	if err != nil {
		return DirectedRun{}, fmt.Errorf("get directed prompt run: %w", err)
	}
	return r, nil
}
