-- +goose Up
CREATE TABLE IF NOT EXISTS agency_websites (
    agency_id uuid PRIMARY KEY REFERENCES agencies (id) ON DELETE CASCADE,
    site_key text NOT NULL
        CONSTRAINT agency_websites_site_key_check CHECK (site_key <> ''),
    CONSTRAINT agency_websites_site_key_unique UNIQUE (site_key)
);

-- +goose StatementBegin
DO $$
DECLARE
    clash record;
BEGIN
    CREATE TEMPORARY TABLE agency_site_keys ON COMMIT DROP AS
    SELECT id, status, created_at, regexp_replace(regexp_replace(lower(website), '^[a-z][a-z0-9+.-]*://(www\.(?=[^/:?#]*\.))?', ''), '[/:?#].*$', '') AS site_key
    FROM agencies;

    FOR clash IN
        SELECT site_key, string_agg(id::text, ', ' ORDER BY created_at, id) AS ids
        FROM agency_site_keys
        WHERE status = 'live' AND site_key <> ''
        GROUP BY site_key
        HAVING count(*) > 1
    LOOP
        RAISE EXCEPTION 'agency_websites backfill: live agencies % share the site %; fix their websites before migrating', clash.ids, clash.site_key;
    END LOOP;

    INSERT INTO agency_websites (agency_id, site_key)
    SELECT DISTINCT ON (site_key) id, site_key
    FROM agency_site_keys
    WHERE site_key <> ''
    ORDER BY site_key, status = 'live' DESC, created_at, id
    ON CONFLICT DO NOTHING;

    FOR clash IN
        SELECT k.id, k.status, k.site_key, w.agency_id AS holder
        FROM agency_site_keys k
        JOIN agency_websites w ON w.site_key = k.site_key AND w.agency_id <> k.id
        ORDER BY k.created_at, k.id
    LOOP
        RAISE NOTICE 'agency_websites backfill: % agency % left without a site row because agency % holds the site %', clash.status, clash.id, clash.holder, clash.site_key;
    END LOOP;
END
$$;
-- +goose StatementEnd

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
