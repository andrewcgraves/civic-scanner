//go:build integration

package store_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func seedLiveMeeting(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	meetingID := seedMeeting(t, pool, slug)
	if _, err := pool.Exec(t.Context(),
		`UPDATE agencies SET status = 'live', website = 'https://example.com' WHERE id = (SELECT agency_id FROM meetings WHERE id = $1)`, meetingID); err != nil {
		t.Fatalf("make agency live: %v", err)
	}
	return meetingID
}

func TestGetLiveMeetingDetailWithNothingYet(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := seedLiveMeeting(t, pool, "store-test-detail-empty")

	d, err := store.New(pool).GetLiveMeetingDetail(ctx, meetingID)
	if err != nil {
		t.Fatalf("GetLiveMeetingDetail: %v", err)
	}
	if d.Meeting.ID != meetingID || d.Meeting.Title != "Board of Directors store-test-detail-empty" || d.Meeting.SourceURL == "" {
		t.Errorf("meeting = %+v", d.Meeting)
	}
	if d.Agency.ID != d.Meeting.AgencyID || d.Agency.Slug != "store-test-detail-empty" || d.Agency.Website != "https://example.com" || d.Agency.CreatedAt.IsZero() {
		t.Errorf("agency = %+v", d.Agency)
	}
	if d.Agenda != nil {
		t.Errorf("agenda = %+v, want nil", d.Agenda)
	}
	if d.Recordings == nil || len(d.Recordings) != 0 {
		t.Errorf("recordings = %#v, want empty non-nil", d.Recordings)
	}
}

func TestGetLiveMeetingDetailWithLatestAgendaAndEveryRecordingState(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedLiveMeeting(t, pool, "store-test-detail-full")

	if _, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "first agenda")); err != nil {
		t.Fatal(err)
	}
	latest, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "second agenda"))
	if err != nil {
		t.Fatal(err)
	}
	if latest, err = s.SetAgendaChangeNote(ctx, latest.ID, "Item 5 added."); err != nil {
		t.Fatal(err)
	}

	summarized, _, err := s.UpsertRecording(ctx, recordingInput(meetingID, "part-1"))
	if err != nil {
		t.Fatal(err)
	}
	unsummarized, _, err := s.UpsertRecording(ctx, recordingInput(meetingID, "part-2"))
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := s.UpsertRecording(ctx, recordingInput(meetingID, "part-3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRecordingAudio(ctx, summarized.ID, store.RecordingAudio{Key: "audio/" + summarized.ID.String() + ".mp3", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	segments := []store.TranscriptSegment{
		{Start: 0, End: 1500 * time.Millisecond, Text: "Call to order.", Speaker: "c0/speaker_0"},
		{Start: 1500 * time.Millisecond, End: 3 * time.Second, Text: "Roll call."},
	}
	text := "Call to order. Roll call."
	first, _, err := s.SaveTranscript(ctx, store.TranscriptInput{RecordingID: summarized.ID, Text: text, Segments: segments})
	if err != nil {
		t.Fatal(err)
	}
	if first, err = s.SetTranscriptSummary(ctx, first.ID, store.HashContent(text), "The board met."); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveTranscript(ctx, store.TranscriptInput{RecordingID: unsummarized.ID, Text: ""}); err != nil {
		t.Fatal(err)
	}

	d, err := s.GetLiveMeetingDetail(ctx, meetingID)
	if err != nil {
		t.Fatalf("GetLiveMeetingDetail: %v", err)
	}

	if a := d.Agenda; a == nil || a.ID != latest.ID || a.Version != 2 || a.SourceURL != latest.SourceURL ||
		a.Summary != nil || a.ChangeNote == nil || *a.ChangeNote != "Item 5 added." || !a.CreatedAt.Equal(latest.CreatedAt) {
		t.Errorf("agenda = %+v, want version 2 %+v", a, latest)
	} else if a.Text != "" {
		t.Errorf("agenda text = %q, want it left unloaded", a.Text)
	}

	var ids []uuid.UUID
	for _, r := range d.Recordings {
		ids = append(ids, r.Recording.ID)
	}
	if want := []uuid.UUID{summarized.ID, unsummarized.ID, pending.ID}; !slices.Equal(ids, want) {
		t.Fatalf("recordings = %v, want %v in the order added", ids, want)
	}

	one := d.Recordings[0]
	if one.Recording.SourceURL != summarized.SourceURL || one.Recording.Audio == nil {
		t.Errorf("first recording = %+v", one.Recording)
	}
	if tr := one.Transcript; tr == nil || tr.ID != first.ID || tr.RecordingID != summarized.ID || tr.Summary == nil || *tr.Summary != "The board met." ||
		!slices.Equal(tr.Segments, segments) || !tr.UpdatedAt.Equal(first.UpdatedAt) || tr.CreatedAt.IsZero() {
		t.Errorf("first transcript = %+v, want %+v", tr, first)
	}

	two := d.Recordings[1]
	if tr := two.Transcript; tr == nil || tr.Summary != nil || tr.Segments == nil || len(tr.Segments) != 0 {
		t.Errorf("second transcript = %+v, want no summary and empty segments", tr)
	}

	if three := d.Recordings[2]; three.Transcript != nil || three.Recording.SourceURL != pending.SourceURL {
		t.Errorf("third recording = %+v, want no transcript", three)
	}
}

func TestGetLiveMeetingDetailNotFoundForUnknownAndNotLive(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	pendingMeeting := seedMeeting(t, pool, "store-test-detail-pending")
	rejectedMeeting := seedMeeting(t, pool, "store-test-detail-rejected")
	if _, err := pool.Exec(ctx,
		`UPDATE agencies SET status = 'rejected' WHERE id = (SELECT agency_id FROM meetings WHERE id = $1)`, rejectedMeeting); err != nil {
		t.Fatal(err)
	}

	for name, id := range map[string]uuid.UUID{"unknown": uuid.New(), "pending agency": pendingMeeting, "rejected agency": rejectedMeeting} {
		if _, err := s.GetLiveMeetingDetail(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestGetLiveMeetingDetailFailsOnUndecodableSegments(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := seedLiveMeeting(t, pool, "store-test-detail-bad-segments")
	r, _, err := store.New(pool).UpsertRecording(ctx, recordingInput(meetingID, "full"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO transcripts (recording_id, full_text, segments) VALUES ($1, '', '[1]')`, r.ID); err != nil {
		t.Fatal(err)
	}

	_, err = store.New(pool).GetLiveMeetingDetail(ctx, meetingID)
	if err == nil || errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "decode segments") {
		t.Fatalf("err = %v, want a decode segments error", err)
	}
}

func TestGetLiveMeetingDetailFailsOnClosedPool(t *testing.T) {
	pool, err := store.Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()

	_, err = store.New(pool).GetLiveMeetingDetail(t.Context(), uuid.New())
	if err == nil || errors.Is(err, store.ErrNotFound) || !strings.HasPrefix(err.Error(), "get meeting detail:") {
		t.Fatalf("err = %v, want a get meeting detail error", err)
	}
}
