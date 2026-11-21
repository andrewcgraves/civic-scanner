-- +goose Up
-- migration: contract
-- squawk-ignore ban-drop-constraint
ALTER TABLE webhook_subscriptions DROP CONSTRAINT IF EXISTS webhook_subscriptions_format_check;

-- +goose Down
UPDATE webhook_subscriptions SET format = 'standard' WHERE format = 'custom';
ALTER TABLE webhook_subscriptions
    ADD CONSTRAINT webhook_subscriptions_format_check
    CHECK (format IN ('standard', 'discord', 'slack'))
    NOT VALID;
