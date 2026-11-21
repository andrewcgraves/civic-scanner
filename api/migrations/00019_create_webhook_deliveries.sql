-- +goose Up
CREATE TABLE IF NOT EXISTS webhook_event_fan_outs (
    event_id uuid PRIMARY KEY REFERENCES meeting_events (id) ON DELETE CASCADE,
    event_sequence bigint NOT NULL,
    deliveries bigint NOT NULL
        CONSTRAINT webhook_event_fan_outs_deliveries_check CHECK (deliveries >= 0),
    fanned_out_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS webhook_event_fan_outs_sequence_idx ON webhook_event_fan_outs (event_sequence);

INSERT INTO webhook_event_fan_outs (event_id, event_sequence, deliveries)
SELECT id, sequence, 0 FROM meeting_events
ON CONFLICT (event_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS webhook_delivery_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id uuid NOT NULL REFERENCES webhook_subscriptions (id) ON DELETE CASCADE,
    event_id uuid NOT NULL REFERENCES meeting_events (id) ON DELETE CASCADE,
    attempt bigint NOT NULL
        CONSTRAINT webhook_delivery_attempts_attempt_check CHECK (attempt > 0),
    started_at timestamptz NOT NULL,
    duration_ms bigint NOT NULL
        CONSTRAINT webhook_delivery_attempts_duration_check CHECK (duration_ms >= 0),
    status_code bigint
        CONSTRAINT webhook_delivery_attempts_status_code_check CHECK (status_code BETWEEN 100 AND 599),
    error text
        CONSTRAINT webhook_delivery_attempts_error_check CHECK (octet_length(error) BETWEEN 1 AND 500),
    succeeded boolean NOT NULL,
    CONSTRAINT webhook_delivery_attempts_outcome_check CHECK (
        (succeeded AND status_code BETWEEN 200 AND 299 AND error IS NULL)
        OR (NOT succeeded AND (status_code IS NOT NULL OR error IS NOT NULL))
    )
);

CREATE INDEX IF NOT EXISTS webhook_delivery_attempts_subscription_idx
    ON webhook_delivery_attempts (subscription_id, started_at DESC, id);

CREATE INDEX IF NOT EXISTS webhook_delivery_attempts_event_idx ON webhook_delivery_attempts (event_id);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS webhook_delivery_attempts;
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS webhook_event_fan_outs;
