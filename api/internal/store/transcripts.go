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

var ErrInvalidSegment = errors.New("invalid transcript segment")

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

func scanTranscript(row pgx.Row, extra ...any) (Transcript, error) {
	var (
		t   Transcript
		raw []byte
	)
	dest := append([]any{&t.ID, &t.RecordingID, &t.Text, &raw, &t.Summary, &t.CreatedAt, &t.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
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
	return saveTranscript(ctx, s.pool, in)
}

func (s *Store) SaveTranscriptTx(ctx context.Context, tx pgx.Tx, in TranscriptInput) (Transcript, bool, error) {
	return saveTranscript(ctx, tx, in)
}

func saveTranscript(ctx context.Context, q querier, in TranscriptInput) (Transcript, bool, error) {
	segments, err := encodeSegments(in.Segments)
	if err != nil {
		return Transcript{}, false, fmt.Errorf("save transcript: %w", err)
	}
	var created bool
	t, err := scanTranscript(q.QueryRow(ctx, `
INSERT INTO transcripts (recording_id, full_text, segments)
VALUES ($1, $2, $3::jsonb)
ON CONFLICT (recording_id) DO UPDATE SET
    full_text = EXCLUDED.full_text,
    segments = EXCLUDED.segments,
    summary = CASE WHEN transcripts.full_text = EXCLUDED.full_text THEN transcripts.summary END,
    updated_at = clock_timestamp()
RETURNING `+transcriptColumns+`, (xmax = 0)`,
		in.RecordingID, in.Text, segments), &created)
	if isForeignKeyViolation(err) {
		return Transcript{}, false, fmt.Errorf("save transcript: recording %s: %w", in.RecordingID, ErrNotFound)
	}
	if err != nil {
		return Transcript{}, false, fmt.Errorf("save transcript: %w", err)
	}
	return t, created, nil
}

func (s *Store) GetTranscript(ctx context.Context, recordingID uuid.UUID) (Transcript, error) {
	t, err := scanTranscript(s.pool.QueryRow(ctx, `SELECT `+transcriptColumns+` FROM transcripts WHERE recording_id = $1`, recordingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, ErrNotFound
	}
	if err != nil {
		return Transcript{}, fmt.Errorf("get transcript: %w", err)
	}
	return t, nil
}

const setTranscriptSummarySQL = `UPDATE transcripts SET summary = $2, updated_at = clock_timestamp() WHERE id = $1 RETURNING ` + transcriptColumns

func (s *Store) SetTranscriptSummary(ctx context.Context, transcriptID uuid.UUID, summary string) (Transcript, error) {
	return setTranscriptSummary(ctx, s.pool, transcriptID, summary)
}

func (s *Store) SetTranscriptSummaryTx(ctx context.Context, tx pgx.Tx, transcriptID uuid.UUID, summary string) (Transcript, error) {
	return setTranscriptSummary(ctx, tx, transcriptID, summary)
}

func setTranscriptSummary(ctx context.Context, q querier, transcriptID uuid.UUID, summary string) (Transcript, error) {
	t, err := scanTranscript(q.QueryRow(ctx, setTranscriptSummarySQL, transcriptID, summary))
	if errors.Is(err, pgx.ErrNoRows) {
		return Transcript{}, fmt.Errorf("set transcript summary: transcript %s: %w", transcriptID, ErrNotFound)
	}
	if err != nil {
		return Transcript{}, fmt.Errorf("set transcript summary: %w", err)
	}
	return t, nil
}
