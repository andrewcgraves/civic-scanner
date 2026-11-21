-- +goose Up
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS prompt text
    CONSTRAINT webhook_subscriptions_prompt_check CHECK (octet_length(prompt) BETWEEN 1 AND 4000);

CREATE TABLE IF NOT EXISTS webhook_directed_prompt_runs (
    subscription_id uuid NOT NULL REFERENCES webhook_subscriptions (id) ON DELETE CASCADE,
    event_id uuid NOT NULL REFERENCES meeting_events (id) ON DELETE CASCADE,
    outcome text
        CONSTRAINT webhook_directed_prompt_runs_outcome_check CHECK (outcome IN ('relevant', 'not_relevant', 'failed')),
    message text
        CONSTRAINT webhook_directed_prompt_runs_message_check CHECK (octet_length(message) BETWEEN 1 AND 4000),
    model text NOT NULL DEFAULT '',
    calls bigint NOT NULL DEFAULT 0
        CONSTRAINT webhook_directed_prompt_runs_calls_check CHECK (calls >= 0),
    input_tokens bigint NOT NULL DEFAULT 0
        CONSTRAINT webhook_directed_prompt_runs_input_tokens_check CHECK (input_tokens >= 0),
    output_tokens bigint NOT NULL DEFAULT 0
        CONSTRAINT webhook_directed_prompt_runs_output_tokens_check CHECK (output_tokens >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subscription_id, event_id),
    CONSTRAINT webhook_directed_prompt_runs_message_outcome_check CHECK ((outcome = 'relevant') = (message IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS webhook_directed_prompt_runs_event_idx ON webhook_directed_prompt_runs (event_id);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS webhook_directed_prompt_runs;
-- squawk-ignore ban-drop-column
ALTER TABLE webhook_subscriptions DROP COLUMN IF EXISTS prompt;
