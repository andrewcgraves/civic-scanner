//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func cleanupMeetingChildren(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		bg := context.WithoutCancel(t.Context())
		_, _ = pool.Exec(bg, `DELETE FROM meeting_events WHERE meeting_id = $1`, meetingID)
		_, _ = pool.Exec(bg, `DELETE FROM transcripts WHERE recording_id IN (SELECT id FROM recordings WHERE meeting_id = $1)`, meetingID)
		_, _ = pool.Exec(bg, `DELETE FROM recordings WHERE meeting_id = $1`, meetingID)
		_, _ = pool.Exec(bg, `DELETE FROM agenda_versions WHERE meeting_id = $1`, meetingID)
	})
}

func TestGetMeetingAgainstPostgres(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "it-meeting-detail", "live")
	empty := seedMeeting(t, pool, agencyID, "detail-empty", time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC))
	full := seedMeeting(t, pool, agencyID, "detail-full", time.Date(2026, 3, 2, 18, 0, 0, 0, time.UTC))
	cleanupMeetingChildren(t, pool, full)
	srv := newServer(t, pool)

	code, body := getBody(t, srv.URL+"/v1/meetings/"+empty.String())
	if code != http.StatusOK || !strings.Contains(body, `"agenda":null,`) || !strings.Contains(body, `"recordings":[],`) ||
		!strings.Contains(body, `"slug":"it-meeting-detail"`) || !strings.Contains(body, `"sourceUrl":"https://example.com/detail-empty"`) {
		t.Errorf("empty meeting = %d %s", code, body)
	}

	agendaText := "Item 1. Item 2."
	v, _, err := s.AddAgendaVersion(ctx, store.AgendaVersionInput{MeetingID: full, SourceURL: "https://example.com/agenda.pdf", Text: agendaText, ContentHash: store.HashContent(agendaText)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAgendaSummary(ctx, v.ID, "Two items."); err != nil {
		t.Fatal(err)
	}
	withTranscript, _, err := s.UpsertRecording(ctx, store.RecordingInput{MeetingID: full, SourceURL: "https://example.com/video/1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertRecording(ctx, store.RecordingInput{MeetingID: full, SourceURL: "https://example.com/video/2"}); err != nil {
		t.Fatal(err)
	}
	audioKey := "audio/" + withTranscript.ID.String() + ".mp3"
	if _, err := s.SetRecordingAudio(ctx, withTranscript.ID, store.RecordingAudio{Key: audioKey, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveTranscript(ctx, store.TranscriptInput{
		RecordingID: withTranscript.ID,
		Text:        "Hello.",
		Segments:    []store.TranscriptSegment{{Start: 250 * time.Millisecond, End: 1250 * time.Millisecond, Text: "Hello.", Speaker: "c0/speaker_1"}},
	}); err != nil {
		t.Fatal(err)
	}

	var detail v1.MeetingDetail
	getJSON(t, srv.URL+"/v1/meetings/"+full.String(), http.StatusOK, &detail)
	if detail.Id != full || detail.Agency.Id != agencyID || detail.Title != "detail-full" || detail.Status != v1.Scheduled {
		t.Errorf("meeting = %+v", detail)
	}
	if a := detail.Agenda; a == nil || a.Version != 1 || a.DocumentUrl != "https://example.com/agenda.pdf" || a.Summary == nil || *a.Summary != "Two items." || a.ChangeNote != nil {
		t.Errorf("agenda = %+v", a)
	}
	if len(detail.Recordings) != 2 {
		t.Fatalf("recordings = %+v, want 2", detail.Recordings)
	}
	first, second := detail.Recordings[0], detail.Recordings[1]
	if first.SourceUrl != "https://example.com/video/1" || first.Transcript == nil || first.Transcript.Summary != nil ||
		len(first.Transcript.Segments) != 1 || first.Transcript.Segments[0].StartMs != 250 || first.Transcript.Segments[0].EndMs != 1250 ||
		first.Transcript.Segments[0].Speaker == nil || *first.Transcript.Segments[0].Speaker != "c0/speaker_1" {
		t.Errorf("first recording = %+v", first)
	}
	if second.SourceUrl != "https://example.com/video/2" || second.Transcript != nil {
		t.Errorf("second recording = %+v", second)
	}

	_, raw := getBody(t, srv.URL+"/v1/meetings/"+full.String())
	for _, hidden := range []string{audioKey, agendaText, "audio", "expires"} {
		if strings.Contains(raw, hidden) {
			t.Errorf("response exposes %q: %s", hidden, raw)
		}
	}
}

func TestGetMeetingNotFoundAgainstPostgres(t *testing.T) {
	pool := testdb.Pool(t)
	pendingAgency := seedAgency(t, pool, "it-meeting-detail-pending", "pending")
	pendingMeeting := seedMeeting(t, pool, pendingAgency, "detail-pending", time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC))
	srv := newServer(t, pool)

	_, unknown := getBody(t, srv.URL+"/v1/meetings/"+uuid.NewString())
	code, notLive := getBody(t, srv.URL+"/v1/meetings/"+pendingMeeting.String())
	if code != http.StatusNotFound || notLive != unknown || !strings.Contains(unknown, `"code":"not_found"`) {
		t.Errorf("pending agency's meeting = %d %q, unknown = %q; want the same 404", code, notLive, unknown)
	}

	var body v1.Error
	getJSON(t, srv.URL+"/v1/meetings/not-a-uuid", http.StatusBadRequest, &body)
	if body.Code != "invalid_parameter" {
		t.Errorf("malformed id: code = %q, want invalid_parameter", body.Code)
	}
}
