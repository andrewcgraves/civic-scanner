-- +goose Up
ALTER TABLE webhook_subscriptions
    ADD COLUMN IF NOT EXISTS consecutive_failed_events bigint NOT NULL DEFAULT 0
        CONSTRAINT webhook_subscriptions_consecutive_failed_events_check CHECK (consecutive_failed_events >= 0);

ALTER TABLE webhook_subscriptions
    ADD COLUMN IF NOT EXISTS paused_reason text
        CONSTRAINT webhook_subscriptions_paused_reason_check CHECK (paused_reason IN ('owner', 'failed_deliveries'));

ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS paused_at timestamptz;

UPDATE webhook_subscriptions SET paused_reason = 'owner', paused_at = updated_at
WHERE status = 'paused' AND paused_reason IS NULL;

-- +goose Down
ALTER TABLE webhook_subscriptions DROP COLUMN IF EXISTS paused_at;
ALTER TABLE webhook_subscriptions DROP COLUMN IF EXISTS paused_reason;
ALTER TABLE webhook_subscriptions DROP COLUMN IF EXISTS consecutive_failed_events;
