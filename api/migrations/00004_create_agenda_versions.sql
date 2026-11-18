-- +goose Up
CREATE TABLE IF NOT EXISTS agenda_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id uuid NOT NULL REFERENCES meetings (id),
    version bigint NOT NULL
        CONSTRAINT agenda_versions_version_check CHECK (version >= 1),
    source_url text NOT NULL
        CONSTRAINT agenda_versions_source_url_check CHECK (source_url <> ''),
    extracted_text text NOT NULL,
    content_hash text NOT NULL
        CONSTRAINT agenda_versions_content_hash_check CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    summary text,
    change_note text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agenda_versions_meeting_version_unique UNIQUE (meeting_id, version)
);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS agenda_versions;
