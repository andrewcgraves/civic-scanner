package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var boardMeeting = store.Meeting{
	ID:        uuid.MustParse("0b6c1f0e-8a8d-4b1e-9b8f-1f2a3b4c5d6e"),
	AgencyID:  laMetro.ID,
	Title:     "Board of Directors",
	StartsAt:  time.Date(2026, 3, 26, 10, 0, 0, 0, time.FixedZone("PDT", -7*3600)),
	Status:    "held",
	SourceURL: "https://metro.legistar.com/m/1",
	SourceID:  "1",
}

const meetingPath = "/v1/meetings/0b6c1f0e-8a8d-4b1e-9b8f-1f2a3b4c5d6e"

const laMetroJSON = `"agency":{"id":"6f1c2a52-3c1e-4d8e-9b55-0a7c1d2e3f40","name":"LA Metro","slug":"la-metro","website":"https://www.metro.net"}`

func strPtr(s string) *string { return &s }

func meetingStore(d store.MeetingDetail) fakeStore {
	s := liveStore()
	s.meeting = &d
	return s
}

func getMeetingBody(t *testing.T, s Store) string {
	t.Helper()
	rec := getRaw(t, s, meetingPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	return rec.Body.String()
}

func TestGetMeetingWithNothingYet(t *testing.T) {
	m := boardMeeting
	m.Status = "scheduled"
	m.SourceURL = ""
	body := getMeetingBody(t, meetingStore(store.MeetingDetail{Meeting: m, Agency: laMetro}))
	want := `{` + laMetroJSON + `,"agenda":null,"id":"0b6c1f0e-8a8d-4b1e-9b8f-1f2a3b4c5d6e","recordings":[],"startsAt":"2026-03-26T17:00:00Z","status":"scheduled","title":"Board of Directors"}` + "\n"
	if body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
}

func TestGetMeetingWithEverything(t *testing.T) {
	published := time.Date(2026, 3, 20, 1, 2, 3, 0, time.FixedZone("PDT", -7*3600))
	transcribed := time.Date(2026, 3, 27, 4, 5, 6, 0, time.UTC)
	d := store.MeetingDetail{
		Meeting: boardMeeting,
		Agency:  laMetro,
		Agenda: &store.AgendaVersion{
			Version:    2,
			SourceURL:  "https://metro.legistar1.com/agenda.pdf",
			Text:       "full agenda text that is never returned",
			Summary:    strPtr("Fare changes."),
			ChangeNote: strPtr("Item 5 added."),
			CreatedAt:  published,
		},
		Recordings: []store.RecordingDetail{
			{
				Recording: store.Recording{
					SourceURL: "https://metro.granicus.com/player/clip/1",
					Audio:     &store.RecordingAudio{Key: "secret/bucket/key.mp3", ExpiresAt: transcribed},
				},
				Transcript: &store.Transcript{
					Text: "full transcript text that is never returned",
					Segments: []store.TranscriptSegment{
						{Start: 0, End: 1500 * time.Millisecond, Text: "Call to order.", Speaker: "c0/speaker_0"},
						{Start: 1500 * time.Millisecond, End: 3 * time.Second, Text: "Roll call."},
					},
					Summary:   strPtr("The board met."),
					UpdatedAt: transcribed,
				},
			},
			{
				Recording: store.Recording{SourceURL: "https://metro.granicus.com/player/clip/2"},
				Transcript: &store.Transcript{
					Segments:  []store.TranscriptSegment{},
					UpdatedAt: transcribed,
				},
			},
			{
				Recording: store.Recording{SourceURL: "https://metro.granicus.com/player/clip/3", AudioDeletedAt: &transcribed},
			},
		},
	}
	body := getMeetingBody(t, meetingStore(d))
	want := `{` + laMetroJSON + `,"agenda":{"changeNote":"Item 5 added.","documentUrl":"https://metro.legistar1.com/agenda.pdf","publishedAt":"2026-03-20T08:02:03Z","summary":"Fare changes.","version":2},` +
		`"id":"0b6c1f0e-8a8d-4b1e-9b8f-1f2a3b4c5d6e","recordings":[` +
		`{"sourceUrl":"https://metro.granicus.com/player/clip/1","transcript":{"segments":[{"endMs":1500,"speaker":"c0/speaker_0","startMs":0,"text":"Call to order."},{"endMs":3000,"startMs":1500,"text":"Roll call."}],"summary":"The board met.","updatedAt":"2026-03-27T04:05:06Z"}},` +
		`{"sourceUrl":"https://metro.granicus.com/player/clip/2","transcript":{"segments":[],"summary":null,"updatedAt":"2026-03-27T04:05:06Z"}},` +
		`{"sourceUrl":"https://metro.granicus.com/player/clip/3","transcript":null}` +
		`],"sourceUrl":"https://metro.legistar.com/m/1","startsAt":"2026-03-26T17:00:00Z","status":"held","title":"Board of Directors"}` + "\n"
	if body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
}

func TestGetMeetingAgendaWithoutSummaryOrChangeNote(t *testing.T) {
	d := store.MeetingDetail{
		Meeting: boardMeeting,
		Agency:  laMetro,
		Agenda:  &store.AgendaVersion{Version: 1, SourceURL: "https://example.com/a.pdf", CreatedAt: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)},
	}
	body := getMeetingBody(t, meetingStore(d))
	want := `,"agenda":{"changeNote":null,"documentUrl":"https://example.com/a.pdf","publishedAt":"2026-03-20T00:00:00Z","summary":null,"version":1},`
	if !strings.Contains(body, want) {
		t.Errorf("body = %s, want it to contain %s", body, want)
	}
}

func TestGetMeetingNotFound(t *testing.T) {
	other := boardMeeting
	other.ID = uuid.New()
	s := meetingStore(store.MeetingDetail{Meeting: other, Agency: laMetro})
	unknown := getRaw(t, s, meetingPath)
	assertError(t, unknown, http.StatusNotFound, "not_found")

	wrapped := liveStore()
	wrapped.meetingErr = fmt.Errorf("get meeting detail: %w", store.ErrNotFound)
	notLive := getRaw(t, wrapped, meetingPath)
	assertError(t, notLive, http.StatusNotFound, "not_found")
	if unknown.Body.String() != notLive.Body.String() {
		t.Errorf("bodies differ: %q vs %q", unknown.Body.String(), notLive.Body.String())
	}
}

func TestGetMeetingRejectsMalformedID(t *testing.T) {
	for _, id := range []string{"not-a-uuid", "123", "0b6c1f0e-8a8d-4b1e-9b8f-1f2a3b4c5d6", "0b6c1f0e-8a8d-4b1e-9b8f-1f2a3b4c5d6e0"} {
		assertError(t, getRaw(t, liveStore(), "/v1/meetings/"+id), http.StatusBadRequest, "invalid_parameter")
	}
}

func TestGetMeetingStoreError(t *testing.T) {
	s := liveStore()
	s.meetingErr = errors.New("boom")
	assertError(t, getRaw(t, s, meetingPath), http.StatusInternalServerError, "internal_error")
}

func TestMeetingsWithoutIDNotFound(t *testing.T) {
	rec := getRaw(t, liveStore(), "/v1/meetings")
	assertError(t, rec, http.StatusNotFound, "not_found")
}
