package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultWebhookDeliveriesLimit = 20
	MaxWebhookDeliveriesLimit     = 100
	deliveryCursorChecksumSize    = 8
)

type ListWebhookDeliveriesParams struct {
	Cursor string
	Limit  int
}

type WebhookDeliveryLogEntry struct {
	EventID        uuid.UUID
	EventType      EventType
	Meeting        Meeting
	FirstAttemptAt time.Time
	LastAttemptAt  time.Time
	Succeeded      bool
	Attempts       []WebhookAttempt
}

type WebhookDeliveryPage struct {
	Deliveries []WebhookDeliveryLogEntry
	NextCursor string
}

type deliveryCursor struct {
	SubscriptionID uuid.UUID `json:"s"`
	FirstAttemptAt time.Time `json:"f"`
	EventID        uuid.UUID `json:"e"`
}

func deliveryCursorChecksum(payload []byte) []byte {
	sum := sha256.Sum256(append([]byte("webhook-deliveries:"), payload...))
	return sum[:deliveryCursorChecksumSize]
}

func encodeDeliveryCursor(subscriptionID uuid.UUID, d WebhookDeliveryLogEntry) string {
	payload, _ := json.Marshal(deliveryCursor{SubscriptionID: subscriptionID, FirstAttemptAt: d.FirstAttemptAt.UTC(), EventID: d.EventID})
	return base64.RawURLEncoding.EncodeToString(append(payload, deliveryCursorChecksum(payload)...))
}

func decodeDeliveryCursor(cursor string, subscriptionID uuid.UUID) (deliveryCursor, error) {
	var c deliveryCursor
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) <= deliveryCursorChecksumSize {
		return c, ErrInvalidCursor
	}
	payload, checksum := raw[:len(raw)-deliveryCursorChecksumSize], raw[len(raw)-deliveryCursorChecksumSize:]
	if !bytes.Equal(checksum, deliveryCursorChecksum(payload)) {
		return c, ErrInvalidCursor
	}
	if err := json.Unmarshal(payload, &c); err != nil || c.FirstAttemptAt.IsZero() || c.EventID == uuid.Nil || c.SubscriptionID != subscriptionID {
		return deliveryCursor{}, ErrInvalidCursor
	}
	return c, nil
}

func clampDeliveriesLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultWebhookDeliveriesLimit
	case limit > MaxWebhookDeliveriesLimit:
		return MaxWebhookDeliveriesLimit
	default:
		return limit
	}
}

func (s *Store) ListWebhookDeliveries(ctx context.Context, userID, subscriptionID uuid.UUID, params ListWebhookDeliveriesParams) (WebhookDeliveryPage, error) {
	var cursor *deliveryCursor
	if params.Cursor != "" {
		c, err := decodeDeliveryCursor(params.Cursor, subscriptionID)
		if err != nil {
			return WebhookDeliveryPage{}, err
		}
		cursor = &c
	}
	limit := clampDeliveriesLimit(params.Limit)

	var owned bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM webhook_subscriptions WHERE id = $1 AND user_id = $2)`, subscriptionID, userID).Scan(&owned)
	if err != nil {
		return WebhookDeliveryPage{}, fmt.Errorf("check webhook subscription: %w", err)
	}
	if !owned {
		return WebhookDeliveryPage{}, ErrNotFound
	}

	var (
		afterAt *time.Time
		afterID *uuid.UUID
	)
	if cursor != nil {
		afterAt, afterID = &cursor.FirstAttemptAt, &cursor.EventID
	}
	rows, err := s.pool.Query(ctx, `
WITH page AS (
    SELECT event_id, min(started_at) AS first_at
    FROM webhook_delivery_attempts
    WHERE subscription_id = $1
    GROUP BY event_id
    HAVING $2::timestamptz IS NULL OR (min(started_at), event_id) < ($2::timestamptz, $3::uuid)
    ORDER BY first_at DESC, event_id DESC
    LIMIT $4
)
SELECT p.event_id, p.first_at, e.type, m.id, m.agency_id, m.title, m.starts_at, m.status, m.source_url,
       a.id, a.attempt, a.started_at, a.duration_ms, a.status_code, a.error, a.succeeded
FROM page p
JOIN meeting_events e ON e.id = p.event_id
JOIN meetings m ON m.id = e.meeting_id
JOIN webhook_delivery_attempts a ON a.subscription_id = $1 AND a.event_id = p.event_id
ORDER BY p.first_at DESC, p.event_id DESC, a.started_at, a.attempt, a.id`, subscriptionID, afterAt, afterID, limit+1)
	if err != nil {
		return WebhookDeliveryPage{}, fmt.Errorf("query webhook deliveries: %w", err)
	}
	defer rows.Close()

	var deliveries []WebhookDeliveryLogEntry
	for rows.Next() {
		var (
			eventID    uuid.UUID
			firstAt    time.Time
			eventType  EventType
			meeting    Meeting
			attempt    WebhookAttempt
			durationMS int64
		)
		if err := rows.Scan(&eventID, &firstAt, &eventType, &meeting.ID, &meeting.AgencyID, &meeting.Title, &meeting.StartsAt, &meeting.Status, &meeting.SourceURL,
			&attempt.ID, &attempt.Attempt, &attempt.StartedAt, &durationMS, &attempt.StatusCode, &attempt.Error, &attempt.Succeeded); err != nil {
			return WebhookDeliveryPage{}, fmt.Errorf("scan webhook deliveries: %w", err)
		}
		attempt.SubscriptionID, attempt.EventID = subscriptionID, eventID
		attempt.Duration = time.Duration(durationMS) * time.Millisecond
		if n := len(deliveries); n == 0 || deliveries[n-1].EventID != eventID {
			deliveries = append(deliveries, WebhookDeliveryLogEntry{EventID: eventID, EventType: eventType, Meeting: meeting, FirstAttemptAt: firstAt})
		}
		d := &deliveries[len(deliveries)-1]
		d.Attempts = append(d.Attempts, attempt)
		d.Succeeded = d.Succeeded || attempt.Succeeded
		if attempt.StartedAt.After(d.LastAttemptAt) {
			d.LastAttemptAt = attempt.StartedAt
		}
	}
	if err := rows.Err(); err != nil {
		return WebhookDeliveryPage{}, fmt.Errorf("read webhook deliveries: %w", err)
	}

	page := WebhookDeliveryPage{Deliveries: deliveries}
	if len(deliveries) > limit {
		page.Deliveries = deliveries[:limit]
		page.NextCursor = encodeDeliveryCursor(subscriptionID, page.Deliveries[limit-1])
	}
	return page, nil
}
