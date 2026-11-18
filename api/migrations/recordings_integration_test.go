//go:build integration

package migrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func insertRecordingMeeting(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	ctx := t.Context()
	agencyID := insertMeetingAgency(t, pool, slug)
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO meetings (agency_id, title, starts_at, source_id) VALUES ($1, 't', now(), $2) RETURNING id`,
		agencyID, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM transcripts WHERE recording_id IN (SELECT id FROM recordings WHERE meeting_id = $1)`, id)
		_, _ = pool.Exec(bg, `DELETE FROM recordings WHERE meeting_id = $1`, id)
	})
	return id
}

func insertRecording(t *testing.T, pool *pgxpool.Pool, meetingID, sourceURL string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO recordings (meeting_id, source_url) VALUES ($1, $2) RETURNING id`, meetingID, sourceURL).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func violatedConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

func TestRecordingsDefaults(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertRecordingMeeting(t, pool, "migration-test-recording-defaults")

	var (
		audioKey                        *string
		expiresSet, deletedSet          bool
		createdSet, updated, seqPresent bool
	)
	if err := pool.QueryRow(ctx, `
INSERT INTO recordings (meeting_id, source_url) VALUES ($1, 'https://example.com/v.mp4')
RETURNING audio_key, audio_expires_at IS NOT NULL, audio_deleted_at IS NOT NULL, seq IS NOT NULL, created_at IS NOT NULL, updated_at IS NOT NULL`,
		meetingID).Scan(&audioKey, &expiresSet, &deletedSet, &seqPresent, &createdSet, &updated); err != nil {
		t.Fatal(err)
	}
	if audioKey != nil || expiresSet || deletedSet || !seqPresent || !createdSet || !updated {
		t.Errorf("audio_key = %v, expiry set = %v, deleted set = %v, seq set = %v, timestamps set = %v %v; want null, false, false, true, true, true",
			audioKey, expiresSet, deletedSet, seqPresent, createdSet, updated)
	}
}

func TestRecordingsSeqIncreasesInInsertOrder(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertRecordingMeeting(t, pool, "migration-test-recording-seq")

	var first, second int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO recordings (meeting_id, source_url) VALUES ($1, 'https://example.com/1.mp4') RETURNING seq`, meetingID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO recordings (meeting_id, source_url) VALUES ($1, 'https://example.com/2.mp4') RETURNING seq`, meetingID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Errorf("seq %d then %d, want increasing", first, second)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO recordings (seq, meeting_id, source_url) VALUES (1, $1, 'https://example.com/3.mp4')`, meetingID); err == nil {
		t.Error("explicit seq was accepted")
	}
}

func TestRecordingsAreUniquePerMeetingSourceURL(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	first := insertRecordingMeeting(t, pool, "migration-test-recording-unique-a")
	second := insertRecordingMeeting(t, pool, "migration-test-recording-unique-b")

	insert := `INSERT INTO recordings (meeting_id, source_url) VALUES ($1, 'https://example.com/v.mp4')`
	if _, err := pool.Exec(ctx, insert, first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, first); violatedConstraint(err) != "recordings_meeting_source_unique" {
		t.Errorf("duplicate (meeting_id, source_url): err = %v, want recordings_meeting_source_unique", err)
	}
	if _, err := pool.Exec(ctx, insert, second); err != nil {
		t.Errorf("same source url for another meeting was rejected: %v", err)
	}
}

func TestRecordingsRejectInvalidRows(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertRecordingMeeting(t, pool, "migration-test-recording-invalid")
	const expiry = "2027-01-01T00:00:00Z"

	for name, tc := range map[string]struct {
		meetingID, sourceURL string
		audioKey             any
		expiresAt, deletedAt any
		constraint           string
	}{
		"empty source url":   {meetingID, "", nil, nil, nil, "recordings_source_url_check"},
		"missing meeting id": {"00000000-0000-0000-0000-000000000001", "https://example.com/v.mp4", nil, nil, nil, "recordings_meeting_id_fkey"},
		"empty audio key":    {meetingID, "https://example.com/a.mp4", "", expiry, nil, "recordings_audio_key_check"},
		"key without expiry": {meetingID, "https://example.com/b.mp4", "audio/b.mp3", nil, nil, "recordings_audio_expiry_check"},
		"expiry without key": {meetingID, "https://example.com/c.mp4", nil, expiry, nil, "recordings_audio_expiry_check"},
		"deleted with audio": {meetingID, "https://example.com/d.mp4", "audio/d.mp3", expiry, expiry, "recordings_audio_deleted_check"},
	} {
		_, err := pool.Exec(ctx,
			`INSERT INTO recordings (meeting_id, source_url, audio_key, audio_expires_at, audio_deleted_at) VALUES ($1, $2, $3, $4::timestamptz, $5::timestamptz)`,
			tc.meetingID, tc.sourceURL, tc.audioKey, tc.expiresAt, tc.deletedAt)
		if got := violatedConstraint(err); got != tc.constraint {
			t.Errorf("%s: err = %v, want a violation of %s", name, err, tc.constraint)
		}
	}

	for name, tc := range map[string]struct {
		sourceURL            string
		audioKey             any
		expiresAt, deletedAt any
	}{
		"audio stored":  {"https://example.com/e.mp4", "audio/e.mp3", expiry, nil},
		"audio deleted": {"https://example.com/f.mp4", nil, nil, expiry},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO recordings (meeting_id, source_url, audio_key, audio_expires_at, audio_deleted_at) VALUES ($1, $2, $3, $4::timestamptz, $5::timestamptz)`,
			meetingID, tc.sourceURL, tc.audioKey, tc.expiresAt, tc.deletedAt); err != nil {
			t.Errorf("%s: valid row was rejected: %v", name, err)
		}
	}
}

func TestTranscriptsDefaultsAndOnePerRecording(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertRecordingMeeting(t, pool, "migration-test-transcript-defaults")
	recordingID := insertRecording(t, pool, meetingID, "https://example.com/v.mp4")

	var (
		segments            string
		summary             *string
		createdSet, updated bool
	)
	if err := pool.QueryRow(ctx, `
INSERT INTO transcripts (recording_id, full_text) VALUES ($1, 'text')
RETURNING segments::text, summary, created_at IS NOT NULL, updated_at IS NOT NULL`,
		recordingID).Scan(&segments, &summary, &createdSet, &updated); err != nil {
		t.Fatal(err)
	}
	if segments != "[]" || summary != nil || !createdSet || !updated {
		t.Errorf("segments = %s, summary = %v, timestamps set = %v %v; want [], null, true, true", segments, summary, createdSet, updated)
	}

	_, err := pool.Exec(ctx, `INSERT INTO transcripts (recording_id, full_text) VALUES ($1, 'again')`, recordingID)
	if got := violatedConstraint(err); got != "transcripts_recording_unique" {
		t.Errorf("second transcript for one recording: err = %v, want transcripts_recording_unique", err)
	}
}

func TestTranscriptsRejectInvalidRows(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertRecordingMeeting(t, pool, "migration-test-transcript-invalid")
	recordingID := insertRecording(t, pool, meetingID, "https://example.com/v.mp4")

	for name, tc := range map[string]struct {
		recordingID string
		segments    string
		constraint  string
	}{
		"object segments":      {recordingID, `{}`, "transcripts_segments_check"},
		"null json segments":   {recordingID, `null`, "transcripts_segments_check"},
		"missing recording id": {"00000000-0000-0000-0000-000000000001", `[]`, "transcripts_recording_id_fkey"},
	} {
		_, err := pool.Exec(ctx,
			`INSERT INTO transcripts (recording_id, full_text, segments) VALUES ($1, 'text', $2::jsonb)`,
			tc.recordingID, tc.segments)
		if got := violatedConstraint(err); got != tc.constraint {
			t.Errorf("%s: err = %v, want a violation of %s", name, err, tc.constraint)
		}
	}
}
