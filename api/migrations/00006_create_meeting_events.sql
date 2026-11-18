-- +goose Up
CREATE TABLE IF NOT EXISTS meeting_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sequence bigint GENERATED ALWAYS AS IDENTITY
        CONSTRAINT meeting_events_sequence_unique UNIQUE,
    type text NOT NULL
        CONSTRAINT meeting_events_type_check CHECK (type IN ('agenda_published', 'agenda_changed', 'transcript_ready')),
    meeting_id uuid NOT NULL REFERENCES meetings (id),
    payload jsonb NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT meeting_events_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS meeting_events_meeting_sequence_idx
    ON meeting_events (meeting_id, sequence);

-- +goose Down
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS meeting_events;
