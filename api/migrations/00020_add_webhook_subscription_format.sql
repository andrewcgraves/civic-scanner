-- +goose Up
ALTER TABLE webhook_subscriptions
    ADD COLUMN IF NOT EXISTS format text NOT NULL DEFAULT 'standard';

ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT webhook_subscriptions_format_check
    CHECK (format IN ('standard', 'discord', 'slack'))
    NOT VALID;

-- +goose Down
ALTER TABLE webhook_subscriptions DROP CONSTRAINT IF EXISTS webhook_subscriptions_format_check;
-- squawk-ignore ban-drop-column
ALTER TABLE webhook_subscriptions DROP COLUMN IF EXISTS format;
