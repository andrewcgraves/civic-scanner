-- +goose Up
ALTER TABLE agencies
    ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'pending'
        CONSTRAINT agencies_status_check CHECK (status IN ('pending', 'live', 'rejected')),
    ADD COLUMN IF NOT EXISTS website text NOT NULL DEFAULT ''
        CONSTRAINT agencies_live_website_check CHECK (status <> 'live' OR website <> '');

INSERT INTO agencies (slug, name, status, website) VALUES
    ('metrolink', 'Metrolink', 'live', 'https://metrolinktrains.com'),
    ('la-metro', 'LA Metro', 'live', 'https://www.metro.net'),
    ('vctc', 'Ventura County Transportation Commission', 'live', 'https://www.goventura.org'),
    ('rctc', 'Riverside County Transportation Commission', 'live', 'https://www.rctc.org'),
    ('sbcta', 'San Bernardino County Transportation Authority', 'live', 'https://www.gosbcta.com'),
    ('octa', 'Orange County Transportation Authority', 'live', 'https://www.octa.net')
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    status = EXCLUDED.status,
    website = EXCLUDED.website;

-- +goose Down
-- squawk-ignore ban-drop-column
ALTER TABLE agencies DROP COLUMN IF EXISTS website, DROP COLUMN IF EXISTS status;
