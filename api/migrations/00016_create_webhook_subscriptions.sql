-- +goose Up
CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    agency_id uuid NOT NULL REFERENCES agencies (id) ON DELETE CASCADE,
    url text NOT NULL
        CONSTRAINT webhook_subscriptions_url_check CHECK (url LIKE 'https://%' AND octet_length(url) <= 2048),
    payload_version text NOT NULL
        CONSTRAINT webhook_subscriptions_payload_version_check CHECK (payload_version IN ('v1')),
    status text NOT NULL DEFAULT 'active'
        CONSTRAINT webhook_subscriptions_status_check CHECK (status IN ('active', 'paused')),
    secret_ciphertext bytea NOT NULL
        CONSTRAINT webhook_subscriptions_secret_ciphertext_check CHECK (octet_length(secret_ciphertext) > 0),
    secret_rotated_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT webhook_subscriptions_user_agency_url_unique UNIQUE (user_id, agency_id, url)
);

CREATE INDEX IF NOT EXISTS webhook_subscriptions_user_created_idx ON webhook_subscriptions (user_id, created_at, id);

CREATE INDEX IF NOT EXISTS webhook_subscriptions_active_agency_idx ON webhook_subscriptions (agency_id) WHERE status = 'active';

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS webhook_subscriptions;
