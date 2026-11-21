package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxWebhookSubscriptionsPerUser = 25
	MaxWebhookPromptChars          = 500
	WebhookPayloadV1               = "v1"
	webhookUniqueConstraint        = "webhook_subscriptions_user_agency_url_unique"
)

type WebhookStatus string

const (
	WebhookActive WebhookStatus = "active"
	WebhookPaused WebhookStatus = "paused"
)

func (s WebhookStatus) Valid() bool { return s == WebhookActive || s == WebhookPaused }

type WebhookPausedReason string

const (
	WebhookPausedByOwner          WebhookPausedReason = "owner"
	WebhookPausedFailedDeliveries WebhookPausedReason = "failed_deliveries"
)

type WebhookFormat string

const (
	WebhookFormatStandard WebhookFormat = "standard"
	WebhookFormatDiscord  WebhookFormat = "discord"
	WebhookFormatSlack    WebhookFormat = "slack"
)

func (f WebhookFormat) Valid() bool {
	return f == WebhookFormatStandard || f == WebhookFormatDiscord || f == WebhookFormatSlack
}

var (
	ErrUnknownAgency              = errors.New("no live agency has this slug")
	ErrWebhookSubscriptionExists  = errors.New("a subscription to this agency at this URL already exists")
	ErrWebhookSubscriptionLimit   = errors.New("too many webhook subscriptions")
	ErrInvalidWebhookSubscription = errors.New("invalid webhook subscription")
)

type WebhookSubscriptionInput struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	AgencySlug       string
	URL              string
	PayloadVersion   string
	Format           WebhookFormat
	SecretCiphertext []byte
	Prompt           *string
}

type WebhookSubscriptionUpdate struct {
	URL            *string
	PayloadVersion *string
	Format         *WebhookFormat
	Status         *WebhookStatus
	Prompt         *string
}

func (u WebhookSubscriptionUpdate) Empty() bool {
	return u.URL == nil && u.PayloadVersion == nil && u.Format == nil && u.Status == nil && u.Prompt == nil
}

type WebhookSubscription struct {
	ID                      uuid.UUID
	UserID                  uuid.UUID
	AgencyID                uuid.UUID
	AgencySlug              string
	AgencyName              string
	URL                     string
	PayloadVersion          string
	Format                  WebhookFormat
	Status                  WebhookStatus
	Prompt                  *string
	PausedReason            *WebhookPausedReason
	PausedAt                *time.Time
	ConsecutiveFailedEvents int
	SecretRotatedAt         time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

const webhookColumns = `w.id, w.user_id, w.agency_id, a.slug, a.name, w.url, w.payload_version, w.format, w.status, w.paused_reason, w.paused_at,
w.consecutive_failed_events, w.prompt, w.secret_rotated_at, w.created_at, w.updated_at`

func scanWebhookSubscription(row pgx.Row) (WebhookSubscription, error) {
	var w WebhookSubscription
	err := row.Scan(&w.ID, &w.UserID, &w.AgencyID, &w.AgencySlug, &w.AgencyName, &w.URL, &w.PayloadVersion, &w.Format, &w.Status,
		&w.PausedReason, &w.PausedAt, &w.ConsecutiveFailedEvents, &w.Prompt, &w.SecretRotatedAt, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return w, err
	}
	if w.Status != WebhookPaused {
		w.PausedReason, w.PausedAt = nil, nil
		return w, nil
	}
	if w.PausedReason == nil {
		reason := WebhookPausedByOwner
		w.PausedReason = &reason
	}
	if w.PausedAt == nil {
		at := w.UpdatedAt
		w.PausedAt = &at
	}
	return w, nil
}

func isWebhookDuplicate(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == uniqueViolation && pgErr.ConstraintName == webhookUniqueConstraint
}

func (s *Store) CreateWebhookSubscription(ctx context.Context, in WebhookSubscriptionInput) (WebhookSubscription, error) {
	if in.Format == "" {
		in.Format = WebhookFormatStandard
	}
	if in.ID == uuid.Nil || in.UserID == uuid.Nil || in.AgencySlug == "" || in.URL == "" || in.PayloadVersion == "" || !in.Format.Valid() || len(in.SecretCiphertext) == 0 {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: %w", ErrInvalidWebhookSubscription)
	}
	var created WebhookSubscription
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, in.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM webhook_subscriptions WHERE user_id = $1`, in.UserID).Scan(&count); err != nil {
			return fmt.Errorf("count webhook subscriptions: %w", err)
		}
		if count >= MaxWebhookSubscriptionsPerUser {
			return ErrWebhookSubscriptionLimit
		}
		var err error
		created, err = scanWebhookSubscription(tx.QueryRow(ctx, `
WITH w AS (
    INSERT INTO webhook_subscriptions (id, user_id, agency_id, url, payload_version, format, secret_ciphertext, prompt)
    SELECT $1, $2, a.id, $4, $5, $6, $7, NULLIF($8, '') FROM agencies a WHERE a.slug = $3 AND a.status = 'live'
    RETURNING *
)
SELECT `+webhookColumns+` FROM w JOIN agencies a ON a.id = w.agency_id`,
			in.ID, in.UserID, in.AgencySlug, in.URL, in.PayloadVersion, in.Format, in.SecretCiphertext, in.Prompt))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrUnknownAgency
		case isWebhookDuplicate(err):
			return ErrWebhookSubscriptionExists
		case err != nil:
			return fmt.Errorf("insert webhook subscription: %w", err)
		}
		return nil
	})
	if errors.Is(err, ErrUnknownAgency) || errors.Is(err, ErrWebhookSubscriptionExists) || errors.Is(err, ErrWebhookSubscriptionLimit) {
		return WebhookSubscription{}, err
	}
	if err != nil {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: %w", err)
	}
	return created, nil
}

func (s *Store) ListWebhookSubscriptions(ctx context.Context, userID uuid.UUID) ([]WebhookSubscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+webhookColumns+` FROM webhook_subscriptions w JOIN agencies a ON a.id = w.agency_id
WHERE w.user_id = $1 ORDER BY w.created_at, w.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("query webhook subscriptions: %w", err)
	}
	subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (WebhookSubscription, error) { return scanWebhookSubscription(row) })
	if err != nil {
		return nil, fmt.Errorf("scan webhook subscriptions: %w", err)
	}
	return subs, nil
}

func (s *Store) GetWebhookSubscription(ctx context.Context, userID, id uuid.UUID) (WebhookSubscription, error) {
	sub, err := scanWebhookSubscription(s.pool.QueryRow(ctx, `SELECT `+webhookColumns+` FROM webhook_subscriptions w JOIN agencies a ON a.id = w.agency_id
WHERE w.id = $1 AND w.user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookSubscription{}, ErrNotFound
	}
	if err != nil {
		return WebhookSubscription{}, fmt.Errorf("get webhook subscription: %w", err)
	}
	return sub, nil
}

func (s *Store) UpdateWebhookSubscription(ctx context.Context, userID, id uuid.UUID, update WebhookSubscriptionUpdate) (WebhookSubscription, error) {
	if update.Empty() {
		return WebhookSubscription{}, fmt.Errorf("update webhook subscription: nothing to change: %w", ErrInvalidWebhookSubscription)
	}
	if update.Format != nil && !update.Format.Valid() {
		return WebhookSubscription{}, fmt.Errorf("update webhook subscription: format %q: %w", *update.Format, ErrInvalidWebhookSubscription)
	}
	sub, err := scanWebhookSubscription(s.pool.QueryRow(ctx, `
WITH w AS (
    UPDATE webhook_subscriptions
    SET url = COALESCE($3, url), payload_version = COALESCE($4, payload_version), format = COALESCE($6, format),
        status = COALESCE($5, status),
        paused_reason = CASE
            WHEN $5::text IS NULL OR $5::text = status THEN paused_reason
            WHEN $5::text = 'paused' THEN 'owner'
        END,
        paused_at = CASE
            WHEN $5::text IS NULL OR $5::text = status THEN paused_at
            WHEN $5::text = 'paused' THEN now()
        END,
        consecutive_failed_events = CASE WHEN $5::text = 'active' THEN 0 ELSE consecutive_failed_events END,
        prompt = CASE WHEN $7::text IS NULL THEN prompt ELSE NULLIF($7, '') END,
        updated_at = now()
    WHERE id = $1 AND user_id = $2
    RETURNING *
)
SELECT `+webhookColumns+` FROM w JOIN agencies a ON a.id = w.agency_id`, id, userID, update.URL, update.PayloadVersion, update.Status, update.Format, update.Prompt))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return WebhookSubscription{}, ErrNotFound
	case isWebhookDuplicate(err):
		return WebhookSubscription{}, ErrWebhookSubscriptionExists
	case err != nil:
		return WebhookSubscription{}, fmt.Errorf("update webhook subscription: %w", err)
	}
	return sub, nil
}

func (s *Store) RotateWebhookSubscriptionSecret(ctx context.Context, userID, id uuid.UUID, ciphertext []byte) (WebhookSubscription, error) {
	if len(ciphertext) == 0 {
		return WebhookSubscription{}, fmt.Errorf("rotate webhook secret: %w", ErrInvalidWebhookSubscription)
	}
	sub, err := scanWebhookSubscription(s.pool.QueryRow(ctx, `
WITH w AS (
    UPDATE webhook_subscriptions
    SET secret_ciphertext = $3, secret_rotated_at = now(), updated_at = now()
    WHERE id = $1 AND user_id = $2
    RETURNING *
)
SELECT `+webhookColumns+` FROM w JOIN agencies a ON a.id = w.agency_id`, id, userID, ciphertext))
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookSubscription{}, ErrNotFound
	}
	if err != nil {
		return WebhookSubscription{}, fmt.Errorf("rotate webhook secret: %w", err)
	}
	return sub, nil
}

func (s *Store) DeleteWebhookSubscription(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete webhook subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) WebhookSubscriptionSecret(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var sealed []byte
	err := s.pool.QueryRow(ctx, `SELECT secret_ciphertext FROM webhook_subscriptions WHERE id = $1`, id).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read webhook secret: %w", err)
	}
	return sealed, nil
}
