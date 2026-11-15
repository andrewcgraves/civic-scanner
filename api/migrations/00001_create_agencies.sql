-- +goose Up
CREATE TABLE IF NOT EXISTS agencies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS agencies;
