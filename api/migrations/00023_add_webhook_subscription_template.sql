-- +goose Up
ALTER TABLE webhook_subscriptions
    ADD COLUMN IF NOT EXISTS template text
        CONSTRAINT webhook_subscriptions_template_size_check CHECK (octet_length(template) <= 8192);

ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT webhook_subscriptions_format_v2_check
    CHECK (format IN ('standard', 'discord', 'slack', 'custom'))
    NOT VALID;

ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT webhook_subscriptions_custom_template_check
    CHECK (format <> 'custom' OR template IS NOT NULL)
    NOT VALID;

-- +goose Down
ALTER TABLE webhook_subscriptions DROP CONSTRAINT IF EXISTS webhook_subscriptions_custom_template_check;
ALTER TABLE webhook_subscriptions DROP CONSTRAINT IF EXISTS webhook_subscriptions_format_v2_check;
-- squawk-ignore ban-drop-column
ALTER TABLE webhook_subscriptions DROP COLUMN IF EXISTS template;
