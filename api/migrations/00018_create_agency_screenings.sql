-- +goose Up
CREATE TABLE IF NOT EXISTS agency_screenings (
    agency_id uuid PRIMARY KEY REFERENCES agencies (id) ON DELETE CASCADE,
    status text NOT NULL
        CONSTRAINT agency_screenings_status_check CHECK (status IN ('complete', 'failed')),
    verdict text
        CONSTRAINT agency_screenings_verdict_check CHECK (verdict IN ('both', 'agendas_only', 'recordings_only', 'neither')),
    evidence jsonb NOT NULL DEFAULT '[]'
        CONSTRAINT agency_screenings_evidence_check CHECK (jsonb_typeof(evidence) = 'array'),
    pages jsonb NOT NULL DEFAULT '[]'
        CONSTRAINT agency_screenings_pages_check CHECK (jsonb_typeof(pages) = 'array'),
    failure text,
    screened_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agency_screenings_outcome_check CHECK (
        (status = 'complete' AND verdict IS NOT NULL AND failure IS NULL)
        OR (status = 'failed' AND verdict IS NULL AND failure IS NOT NULL AND evidence = '[]')
    )
);

-- +goose Down
DROP TABLE IF EXISTS agency_screenings;
