package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidSegment    = errors.New("invalid transcript segment")
	ErrTranscriptChanged = errors.New("transcript text changed")
)

type TranscriptSegment struct {
	Start   time.Duration
	End     time.Duration
	Text    string
	Speaker string
}

type TranscriptInput struct {
	RecordingID uuid.UUID
	Text        string
	Segments    []TranscriptSegment
}

type Transcript struct {
	ID          uuid.UUID
	RecordingID uuid.UUID
	Text        string
	Segments    []TranscriptSegment
	Summary     *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type segmentJSON struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
}

func encodeSegments(segments []TranscriptSegment) (string, error) {
	out := make([]segmentJSON, len(segments))
	for i, s := range segments {
		if s.Start < 0 || s.End < s.Start {
			return "", fmt.Errorf("segment %d (%v to %v): %w", i, s.Start, s.End, ErrInvalidSegment)
		}
		out[i] = segmentJSON{
			StartMS: s.Start.Round(time.Millisecond).Milliseconds(),
			EndMS:   s.End.Round(time.Millisecond).Milliseconds(),
			Text:    s.Text,
			Speaker: s.Speaker,
		}
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

func decodeSegments(raw []byte) ([]TranscriptSegment, error) {
	var in []segmentJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("decode segments: %w", err)
	}
	segments := make([]TranscriptSegment, len(in))
	for i, s := range in {
		segments[i] = TranscriptSegment{
			Start:   time.Duration(s.StartMS) * time.Millisecond,
			End:     time.Duration(s.EndMS) * time.Millisecond,
			Text:    s.Text,
			Speaker: s.Speaker,
		}
	}
	return segments, nil
}

const transcriptColumns = `id, recording_id, full_text, segments, summary, created_at, updated_at`

func scanTranscript(row pgx.Row) (Transcript, error) {
	var (
		t   Transcript
		raw []byte
	)
	if err := row.Scan(&t.ID, &t.RecordingID, &t.Text, &raw, &t.Summary, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Transcript{}, err
	}
	segments, err := decodeSegments(raw)
	if err != nil {
		return Transcript{}, err
	}
	t.Segments = segments
	return t, nil
}

func (s *Store) SaveTranscript(ctx context.Context, in TranscriptInput) (Transcript, bool, error) {
	segments, err := encodeSegments(in.Segments)
	if err != nil {
		return Transcript{}, false, fmt.Errorf("save transcript: %w", err)
	}
	var (
		t       Transcript
		created bool
	)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		t, created, err = saveTranscript(ctx, tx, in, segments)
		return err
	})
	if err != nil {
		return Transcript{}, false, fmt.Errorf("save transcript: %w", err)
	}
	return t, created, nil
}

func (s *Store) SaveTranscriptTx(ctx context.Context, tx pgx.Tx, in TranscriptInput) (Transcript, bool, error) {
	segments, err := encodeSegments(in.Segments)
	if err != nil {
		return Transcript{}, false, fmt.Errorf("save transcript: %w", err)
	}
	t, created, err := saveTranscript(ctx, tx, in, segments)
	if err != nil {
		return Transcript{}, false, fmt.Errorf("save transcript: %w", err)
	}
	return t, created, nil
}

func saveTranscript(ctx context.Context, q querier, in TranscriptInput, segments string) (Transcript, bool, error) {
	var recordingID uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM recordings WHERE id = $1 FOR KEY SHARE`, in.RecordingID).Scan(&recordingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, false, fmt.Errorf("recording %s: %w", in.RecordingID, ErrNotFound)
	}
	if err != nil {
		return Transcript{}, false, fmt.Errorf("lock recording: %w", err)
	}

	var created bool
	err = q.QueryRow(ctx, `
INSERT INTO transcripts (recording_id, full_text, segments)
VALUES ($1, $2, $3::jsonb)
ON CONFLICT (recording_id) DO UPDATE SET
    full_text = EXCLUDED.full_text,
    segments = EXCLUDED.segments,
    summary = CASE WHEN transcripts.full_text = EXCLUDED.full_text THEN transcripts.summary END,
    updated_at = clock_timestamp()
WHERE transcripts.full_text IS DISTINCT FROM EXCLUDED.full_text
   OR transcripts.segments IS DISTINCT FROM EXCLUDED.segments
RETURNING (xmax = 0)`,
		in.RecordingID, in.Text, segments).Scan(&created)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, false, fmt.Errorf("upsert transcript: %w", err)
	}

	t, err := getTranscript(ctx, q, in.RecordingID)
	if err != nil {
		return Transcript{}, false, err
	}
	return t, created, nil
}

func (s *Store) AddTranscriptTx(ctx context.Context, tx pgx.Tx, in TranscriptInput) (Transcript, bool, error) {
	segments, err := encodeSegments(in.Segments)
	if err != nil {
		return Transcript{}, false, fmt.Errorf("add transcript: %w", err)
	}
	var recordingID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM recordings WHERE id = $1 FOR KEY SHARE`, in.RecordingID).Scan(&recordingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, false, fmt.Errorf("add transcript: recording %s: %w", in.RecordingID, ErrNotFound)
	}
	if err != nil {
		return Transcript{}, false, fmt.Errorf("add transcript: lock recording: %w", err)
	}
	t, err := scanTranscript(tx.QueryRow(ctx, `
INSERT INTO transcripts (recording_id, full_text, segments)
VALUES ($1, $2, $3::jsonb)
ON CONFLICT (recording_id) DO NOTHING
RETURNING `+transcriptColumns, in.RecordingID, in.Text, segments))
	if err == nil {
		return t, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, false, fmt.Errorf("add transcript: %w", err)
	}
	t, err = getTranscript(ctx, tx, in.RecordingID)
	if err != nil {
		return Transcript{}, false, fmt.Errorf("add transcript: %w", err)
	}
	return t, false, nil
}

type UntranscribedParams struct {
	FailedKind   string
	FailedWithin time.Duration
	Limit        int
}

func (s *Store) ListUntranscribedRecordings(ctx context.Context, p UntranscribedParams) ([]uuid.UUID, error) {
	if p.Limit <= 0 {
		return nil, fmt.Errorf("list untranscribed recordings: limit %d: %w", p.Limit, ErrInvalidLimit)
	}
	rows, err := s.pool.Query(ctx, `
SELECT r.id
FROM recordings r
JOIN meetings m ON m.id = r.meeting_id
JOIN agencies a ON a.id = m.agency_id AND a.status = 'live'
WHERE r.audio_key IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM transcripts t WHERE t.recording_id = r.id)
  AND NOT EXISTS (
    SELECT 1 FROM river_job j
    WHERE j.kind = $1
      AND j.args->>'recording_id' = r.id::text
      AND j.state IN ('cancelled', 'discarded')
      AND j.finalized_at >= now() - $2::interval
  )
ORDER BY m.starts_at DESC, r.seq
LIMIT $3`, p.FailedKind, p.FailedWithin, p.Limit)
	if err != nil {
		return nil, fmt.Errorf("list untranscribed recordings: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("scan untranscribed recordings: %w", err)
	}
	return ids, nil
}

func (s *Store) GetTranscript(ctx context.Context, recordingID uuid.UUID) (Transcript, error) {
	return getTranscript(ctx, s.pool, recordingID)
}

func getTranscript(ctx context.Context, q querier, recordingID uuid.UUID) (Transcript, error) {
	t, err := scanTranscript(q.QueryRow(ctx, `SELECT `+transcriptColumns+` FROM transcripts WHERE recording_id = $1`, recordingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, ErrNotFound
	}
	if err != nil {
		return Transcript{}, fmt.Errorf("get transcript: %w", err)
	}
	return t, nil
}

const setTranscriptSummarySQL = `
UPDATE transcripts SET summary = $3, updated_at = clock_timestamp()
WHERE id = $1 AND encode(sha256(convert_to(full_text, 'UTF8')), 'hex') = $2
RETURNING ` + transcriptColumns

func (s *Store) SetTranscriptSummary(ctx context.Context, transcriptID uuid.UUID, summarized ContentHash, summary string) (Transcript, error) {
	return setTranscriptSummary(ctx, s.pool, transcriptID, summarized, summary)
}

func (s *Store) SetTranscriptSummaryTx(ctx context.Context, tx pgx.Tx, transcriptID uuid.UUID, summarized ContentHash, summary string) (Transcript, error) {
	return setTranscriptSummary(ctx, tx, transcriptID, summarized, summary)
}

func setTranscriptSummary(ctx context.Context, q querier, transcriptID uuid.UUID, summarized ContentHash, summary string) (Transcript, error) {
	if summarized.IsZero() {
		return Transcript{}, fmt.Errorf("set transcript summary: %w", ErrZeroContentHash)
	}
	t, err := scanTranscript(q.QueryRow(ctx, setTranscriptSummarySQL, transcriptID, summarized.String(), summary))
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, fmt.Errorf("set transcript summary: %w", err)
	}
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transcripts WHERE id = $1)`, transcriptID).Scan(&exists); err != nil {
		return Transcript{}, fmt.Errorf("set transcript summary: %w", err)
	}
	if !exists {
		return Transcript{}, fmt.Errorf("set transcript summary: transcript %s: %w", transcriptID, ErrNotFound)
	}
	return Transcript{}, fmt.Errorf("set transcript summary: transcript %s: %w", transcriptID, ErrTranscriptChanged)
}

func (s *Store) GetTranscriptByID(ctx context.Context, transcriptID uuid.UUID) (Transcript, error) {
	return getTranscriptByID(ctx, s.pool, transcriptID)
}

func getTranscriptByID(ctx context.Context, q querier, transcriptID uuid.UUID) (Transcript, error) {
	t, err := scanTranscript(q.QueryRow(ctx, `SELECT `+transcriptColumns+` FROM transcripts WHERE id = $1`, transcriptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, fmt.Errorf("transcript %s: %w", transcriptID, ErrNotFound)
	}
	if err != nil {
		return Transcript{}, fmt.Errorf("get transcript: %w", err)
	}
	return t, nil
}

const recordTranscriptSummarySQL = `
UPDATE transcripts SET summary = $3, updated_at = clock_timestamp()
WHERE id = $1 AND summary IS NULL AND encode(sha256(convert_to(full_text, 'UTF8')), 'hex') = $2
RETURNING ` + transcriptColumns

func (s *Store) RecordTranscriptSummary(ctx context.Context, transcriptID uuid.UUID, summarized ContentHash, summary string) (Transcript, bool, error) {
	if summarized.IsZero() {
		return Transcript{}, false, fmt.Errorf("record transcript summary: %w", ErrZeroContentHash)
	}
	var (
		t        Transcript
		recorded bool
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		t, err = scanTranscript(tx.QueryRow(ctx, recordTranscriptSummarySQL, transcriptID, summarized.String(), summary))
		if errors.Is(err, pgx.ErrNoRows) {
			t, err = getTranscriptByID(ctx, tx, transcriptID)
			if err == nil && t.Summary == nil {
				err = fmt.Errorf("transcript %s: %w", transcriptID, ErrTranscriptChanged)
			}
			return err
		}
		if err != nil {
			return fmt.Errorf("set summary: %w", err)
		}
		var meetingID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT meeting_id FROM recordings WHERE id = $1`, t.RecordingID).Scan(&meetingID); err != nil {
			return fmt.Errorf("meeting of recording %s: %w", t.RecordingID, err)
		}
		if _, err := recordMeetingEvent(ctx, tx, MeetingEventInput{
			Type:      EventTranscriptReady,
			MeetingID: meetingID,
			Payload:   TranscriptEventPayload{TranscriptID: t.ID, RecordingID: t.RecordingID},
		}); err != nil {
			return fmt.Errorf("record transcript event: %w", err)
		}
		recorded = true
		return nil
	})
	if err != nil {
		return Transcript{}, false, fmt.Errorf("record transcript summary: %w", err)
	}
	return t, recorded, nil
}

type UnsummarizedTranscriptParams struct {
	From         time.Time
	To           time.Time
	FailedKind   string
	FailedWithin time.Duration
	Limit        int
}

func (s *Store) ListUnsummarizedTranscripts(ctx context.Context, p UnsummarizedTranscriptParams) ([]uuid.UUID, error) {
	if p.Limit <= 0 {
		return nil, fmt.Errorf("list unsummarized transcripts: limit %d: %w", p.Limit, ErrInvalidLimit)
	}
	rows, err := s.pool.Query(ctx, `
SELECT t.id
FROM transcripts t
JOIN recordings r ON r.id = t.recording_id
JOIN meetings m ON m.id = r.meeting_id
JOIN agencies a ON a.id = m.agency_id AND a.status = 'live'
WHERE t.summary IS NULL
  AND m.starts_at >= $1 AND m.starts_at <= $2
  AND NOT EXISTS (
    SELECT 1 FROM river_job j
    WHERE j.kind = $3
      AND j.args->>'transcript_id' = t.id::text
      AND j.state IN ('cancelled', 'discarded')
      AND j.finalized_at >= now() - $4::interval
  )
ORDER BY m.starts_at DESC, r.seq
LIMIT $5`, p.From, p.To, p.FailedKind, p.FailedWithin, p.Limit)
	if err != nil {
		return nil, fmt.Errorf("list unsummarized transcripts: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("scan unsummarized transcripts: %w", err)
	}
	return ids, nil
}
