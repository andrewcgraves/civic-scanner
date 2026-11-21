-- +goose Up
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS plan text NOT NULL DEFAULT 'free'
        CONSTRAINT users_plan_check CHECK (plan IN ('free', 'paid'));

CREATE TABLE IF NOT EXISTS stripe_events (
    id text PRIMARY KEY
        CONSTRAINT stripe_events_id_check CHECK (id <> ''),
    type text NOT NULL,
    created_at timestamptz NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS stripe_subscriptions (
    id text PRIMARY KEY
        CONSTRAINT stripe_subscriptions_id_check CHECK (id <> ''),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    customer_id text NOT NULL
        CONSTRAINT stripe_subscriptions_customer_id_check CHECK (customer_id <> ''),
    status text NOT NULL
        CONSTRAINT stripe_subscriptions_status_check CHECK (status <> ''),
    paid boolean NOT NULL,
    event_id text NOT NULL,
    event_created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS stripe_subscriptions_user_id_idx ON stripe_subscriptions (user_id);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS stripe_subscriptions;
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS stripe_events;
-- squawk-ignore ban-drop-column
ALTER TABLE users DROP COLUMN IF EXISTS plan;
