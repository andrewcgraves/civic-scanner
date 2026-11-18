-- +goose Up
ALTER TABLE recordings
    ADD COLUMN IF NOT EXISTS download_attempts bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS download_failure text,
    ADD COLUMN IF NOT EXISTS download_failed_at timestamptz,
    ADD COLUMN IF NOT EXISTS download_retry_after timestamptz;

ALTER TABLE recordings
    ADD CONSTRAINT recordings_download_failure_check
    CHECK (download_failure IN ('unsupported', 'no_media', 'no_audio', 'malformed', 'refused', 'limit', 'unavailable', 'incomplete', 'transient'))
    NOT VALID;

-- +goose Down
ALTER TABLE recordings DROP CONSTRAINT IF EXISTS recordings_download_failure_check;
-- squawk-ignore ban-drop-column
ALTER TABLE recordings
    DROP COLUMN IF EXISTS download_retry_after,
    DROP COLUMN IF EXISTS download_failed_at,
    DROP COLUMN IF EXISTS download_failure,
    DROP COLUMN IF EXISTS download_attempts;
