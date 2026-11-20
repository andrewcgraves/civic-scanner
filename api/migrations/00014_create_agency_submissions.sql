-- +goose Up
CREATE TABLE IF NOT EXISTS agency_websites (
    agency_id uuid PRIMARY KEY REFERENCES agencies (id) ON DELETE CASCADE,
    site_key text NOT NULL
        CONSTRAINT agency_websites_site_key_check CHECK (site_key <> ''),
    CONSTRAINT agency_websites_site_key_unique UNIQUE (site_key)
);

INSERT INTO agency_websites (agency_id, site_key)
SELECT id, key
FROM (
    SELECT id, created_at, regexp_replace(regexp_replace(lower(website), '^[a-z][a-z0-9+.-]*://(www\.(?=[^/:?#]*\.))?', ''), '[/:?#].*$', '') AS key
    FROM agencies
) keyed
WHERE key <> ''
ORDER BY created_at, id
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS agency_submissions (
    agency_id uuid PRIMARY KEY REFERENCES agencies (id) ON DELETE CASCADE,
    submitted_by uuid NOT NULL REFERENCES users (id),
    submitted_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS agency_submissions_submitted_by_idx ON agency_submissions (submitted_by);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS agency_submissions;
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS agency_websites;
