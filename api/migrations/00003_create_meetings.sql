-- +goose Up
CREATE TABLE IF NOT EXISTS meetings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agency_id uuid NOT NULL REFERENCES agencies (id),
    title text NOT NULL,
    starts_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'scheduled'
        CONSTRAINT meetings_status_check CHECK (status IN ('scheduled', 'held', 'cancelled')),
    source_url text NOT NULL DEFAULT '',
    source_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT meetings_agency_source_unique UNIQUE (agency_id, source_id)
);

CREATE INDEX IF NOT EXISTS meetings_agency_starts_at_idx
    ON meetings (agency_id, starts_at DESC, id DESC);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS meetings;
