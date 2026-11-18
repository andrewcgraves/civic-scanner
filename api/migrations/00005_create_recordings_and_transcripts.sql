-- +goose Up
CREATE TABLE IF NOT EXISTS recordings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id uuid NOT NULL REFERENCES meetings (id),
    source_url text NOT NULL
        CONSTRAINT recordings_source_url_check CHECK (source_url <> ''),
    audio_key text
        CONSTRAINT recordings_audio_key_check CHECK (audio_key <> ''),
    audio_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT recordings_meeting_source_unique UNIQUE (meeting_id, source_url),
    CONSTRAINT recordings_audio_expiry_check CHECK ((audio_key IS NULL) = (audio_expires_at IS NULL))
);

CREATE INDEX IF NOT EXISTS recordings_audio_expires_at_idx
    ON recordings (audio_expires_at, id)
    WHERE audio_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS transcripts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    recording_id uuid NOT NULL REFERENCES recordings (id),
    full_text text NOT NULL,
    segments jsonb NOT NULL DEFAULT '[]'
        CONSTRAINT transcripts_segments_check CHECK (jsonb_typeof(segments) = 'array'),
    summary text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT transcripts_recording_unique UNIQUE (recording_id)
);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS transcripts;
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS recordings;
