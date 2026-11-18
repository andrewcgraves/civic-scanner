-- +goose Up
CREATE TABLE IF NOT EXISTS users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workos_user_id text NOT NULL
        CONSTRAINT users_workos_user_id_check CHECK (workos_user_id <> ''),
    email text
        CONSTRAINT users_email_check CHECK (email <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_workos_user_id_unique UNIQUE (workos_user_id)
);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS users;
