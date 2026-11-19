-- +goose Up
ALTER TABLE agenda_versions
    ADD COLUMN IF NOT EXISTS change_event_pending boolean NOT NULL DEFAULT false;

-- +goose Down
-- squawk-ignore ban-drop-column
ALTER TABLE agenda_versions DROP COLUMN IF EXISTS change_event_pending;
