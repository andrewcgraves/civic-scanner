//go:build integration

package migrations_test

import (
	"context"
	"testing"

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

func TestRecordingsDefaults(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertRecordingMeeting(t, pool, "migration-test-recording-defaults")

	var (
		audioKey            *string
		createdSet, updated bool
		expiresSet          bool
	)
	if err := pool.QueryRow(ctx, `
INSERT INTO recordings (meeting_id, source_url) VALUES ($1, 'https://example.com/v.mp4')
RETURNING audio_key, audio_expires_at IS NOT NULL, created_at IS NOT NULL, updated_at IS NOT NULL`,
		meetingID).Scan(&audioKey, &expiresSet, &createdSet, &updated); err != nil {
		t.Fatal(err)
	}
	if audioKey != nil || expiresSet || !createdSet || !updated {
		t.Errorf("audio_key = %v, expiry set = %v, timestamps set = %v %v; want null, false, true, true", audioKey, expiresSet, createdSet, updated)
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
	if _, err := pool.Exec(ctx, insert, first); err == nil {
		t.Error("duplicate (meeting_id, source_url) was accepted")
	}
	if _, err := pool.Exec(ctx, insert, second); err != nil {
		t.Errorf("same source url for another meeting was rejected: %v", err)
	}
}

func TestRecordingsRejectInvalidRows(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertRecordingMeeting(t, pool, "migration-test-recording-invalid")

	for name, tc := range map[string]struct {
		meetingID, sourceURL string
		audioKey             any
		expiresAt            any
	}{
		"empty source url":   {meetingID, "", nil, nil},
		"missing meeting id": {"00000000-0000-0000-0000-000000000001", "https://example.com/v.mp4", nil, nil},
		"empty audio key":    {meetingID, "https://example.com/a.mp4", "", "2027-01-01T00:00:00Z"},
		"key without expiry": {meetingID, "https://example.com/b.mp4", "audio/b.mp3", nil},
		"expiry without key": {meetingID, "https://example.com/c.mp4", nil, "2027-01-01T00:00:00Z"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO recordings (meeting_id, source_url, audio_key, audio_expires_at) VALUES ($1, $2, $3, $4::timestamptz)`,
			tc.meetingID, tc.sourceURL, tc.audioKey, tc.expiresAt); err == nil {
			t.Errorf("%s: row was accepted", name)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO recordings (meeting_id, source_url, audio_key, audio_expires_at) VALUES ($1, 'https://example.com/d.mp4', 'audio/d.mp3', '2027-01-01T00:00:00Z')`,
		meetingID); err != nil {
		t.Errorf("recording with key and expiry was rejected: %v", err)
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

	if _, err := pool.Exec(ctx, `INSERT INTO transcripts (recording_id, full_text) VALUES ($1, 'again')`, recordingID); err == nil {
		t.Error("second transcript for one recording was accepted")
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
	}{
		"object segments":      {recordingID, `{}`},
		"null json segments":   {recordingID, `null`},
		"missing recording id": {"00000000-0000-0000-0000-000000000001", `[]`},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO transcripts (recording_id, full_text, segments) VALUES ($1, 'text', $2::jsonb)`,
			tc.recordingID, tc.segments); err == nil {
			t.Errorf("%s: row was accepted", name)
		}
	}
}
