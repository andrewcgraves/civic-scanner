package store

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxWebhookAttemptErrorBytes = 500

type FanOutParams struct {
	Limit    int
	Lookback int64
}

func (s *Store) ListEventsAwaitingFanOut(ctx context.Context, p FanOutParams) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT e.id FROM meeting_events e
WHERE e.sequence > (SELECT COALESCE(max(event_sequence), 0) FROM webhook_event_fan_outs) - $2
  AND NOT EXISTS (SELECT 1 FROM webhook_event_fan_outs f WHERE f.event_id = e.id)
ORDER BY e.sequence
LIMIT $1`, p.Limit, p.Lookback)
	if err != nil {
		return nil, fmt.Errorf("query events awaiting fan-out: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("scan events awaiting fan-out: %w", err)
	}
	return ids, nil
}

type WebhookTarget struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	WorkOSUserID   string
}

type WebhookFanOut struct {
	EventID  uuid.UUID
	Sequence int64
	Targets  []WebhookTarget
}

func (s *Store) WebhookFanOutTargets(ctx context.Context, eventID uuid.UUID) (WebhookFanOut, error) {
	fo := WebhookFanOut{EventID: eventID}
	var agencyID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT e.sequence, m.agency_id FROM meeting_events e JOIN meetings m ON m.id = e.meeting_id WHERE e.id = $1`,
		eventID).Scan(&fo.Sequence, &agencyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookFanOut{}, ErrNotFound
	}
	if err != nil {
		return WebhookFanOut{}, fmt.Errorf("read event for fan-out: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
SELECT w.id, u.id, u.workos_user_id
FROM webhook_subscriptions w JOIN users u ON u.id = w.user_id
WHERE w.agency_id = $1 AND w.status = 'active' AND u.plan = 'paid'
ORDER BY w.created_at, w.id`, agencyID)
	if err != nil {
		return WebhookFanOut{}, fmt.Errorf("query webhook targets: %w", err)
	}
	fo.Targets, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (WebhookTarget, error) {
		var t WebhookTarget
		err := row.Scan(&t.SubscriptionID, &t.UserID, &t.WorkOSUserID)
		return t, err
	})
	if err != nil {
		return WebhookFanOut{}, fmt.Errorf("scan webhook targets: %w", err)
	}
	return fo, nil
}

func (s *Store) ClaimWebhookFanOutTx(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, sequence int64, deliveries int) (bool, error) {
	tag, err := tx.Exec(ctx, `
INSERT INTO webhook_event_fan_outs (event_id, event_sequence, deliveries) VALUES ($1, $2, $3)
ON CONFLICT (event_id) DO NOTHING`, eventID, sequence, deliveries)
	if err != nil {
		return false, fmt.Errorf("claim webhook fan-out: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

type WebhookDelivery struct {
	SubscriptionID   uuid.UUID
	UserID           uuid.UUID
	WorkOSUserID     string
	Paid             bool
	URL              string
	PayloadVersion   string
	Format           WebhookFormat
	Status           WebhookStatus
	SecretCiphertext []byte
	Event            MeetingEvent
	AgencySlug       string
	AgencyName       string
	Meeting          Meeting
}

func (s *Store) WebhookDelivery(ctx context.Context, subscriptionID, eventID uuid.UUID) (WebhookDelivery, error) {
	var (
		d       WebhookDelivery
		payload []byte
	)
	err := s.pool.QueryRow(ctx, `
SELECT w.id, u.id, u.workos_user_id, u.plan = 'paid', w.url, w.payload_version, w.format, w.status, w.secret_ciphertext,
       e.id, e.sequence, e.type, e.meeting_id, e.payload, e.occurred_at,
       a.slug, a.name, m.id, m.agency_id, m.title, m.starts_at, m.status, m.source_url
FROM webhook_subscriptions w
JOIN users u ON u.id = w.user_id
JOIN meeting_events e ON e.id = $2
JOIN meetings m ON m.id = e.meeting_id AND m.agency_id = w.agency_id
JOIN agencies a ON a.id = m.agency_id
WHERE w.id = $1`, subscriptionID, eventID).Scan(
		&d.SubscriptionID, &d.UserID, &d.WorkOSUserID, &d.Paid, &d.URL, &d.PayloadVersion, &d.Format, &d.Status, &d.SecretCiphertext,
		&d.Event.ID, &d.Event.Sequence, &d.Event.Type, &d.Event.MeetingID, &payload, &d.Event.OccurredAt,
		&d.AgencySlug, &d.AgencyName, &d.Meeting.ID, &d.Meeting.AgencyID, &d.Meeting.Title, &d.Meeting.StartsAt, &d.Meeting.Status, &d.Meeting.SourceURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookDelivery{}, ErrNotFound
	}
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("read webhook delivery: %w", err)
	}
	d.Event.Payload = payload
	return d, nil
}

type WebhookAttemptInput struct {
	SubscriptionID uuid.UUID
	EventID        uuid.UUID
	Attempt        int
	StartedAt      time.Time
	Duration       time.Duration
	StatusCode     int
	Error          string
	Succeeded      bool
}

type WebhookAttempt struct {
	ID             uuid.UUID
	SubscriptionID uuid.UUID
	EventID        uuid.UUID
	Attempt        int
	StartedAt      time.Time
	Duration       time.Duration
	StatusCode     *int
	Error          *string
	Succeeded      bool
}

func (s *Store) RecordWebhookAttempt(ctx context.Context, in WebhookAttemptInput) error {
	var (
		status  *int
		message *string
	)
	if in.StatusCode != 0 {
		status = &in.StatusCode
	}
	if text := truncateUTF8(in.Error, maxWebhookAttemptErrorBytes); text != "" && !in.Succeeded {
		message = &text
	}
	if !in.Succeeded && status == nil && message == nil {
		text := "the delivery failed"
		message = &text
	}
	duration := max(in.Duration.Milliseconds(), 0)
	_, err := s.pool.Exec(ctx, `
INSERT INTO webhook_delivery_attempts (subscription_id, event_id, attempt, started_at, duration_ms, status_code, error, succeeded)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		in.SubscriptionID, in.EventID, max(in.Attempt, 1), in.StartedAt, duration, status, message, in.Succeeded)
	if err != nil {
		return fmt.Errorf("record webhook attempt: %w", err)
	}
	return nil
}

func (s *Store) ListWebhookAttempts(ctx context.Context, subscriptionID uuid.UUID) ([]WebhookAttempt, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, subscription_id, event_id, attempt, started_at, duration_ms, status_code, error, succeeded
FROM webhook_delivery_attempts WHERE subscription_id = $1 ORDER BY started_at DESC, attempt DESC, id`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("query webhook attempts: %w", err)
	}
	attempts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (WebhookAttempt, error) {
		var (
			a          WebhookAttempt
			durationMS int64
		)
		err := row.Scan(&a.ID, &a.SubscriptionID, &a.EventID, &a.Attempt, &a.StartedAt, &durationMS, &a.StatusCode, &a.Error, &a.Succeeded)
		a.Duration = time.Duration(durationMS) * time.Millisecond
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan webhook attempts: %w", err)
	}
	return attempts, nil
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
