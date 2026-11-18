package legistar_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/legistar"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

func octa(t *testing.T, fetcher fetch.Fetcher, now time.Time, logs *bytes.Buffer) *legistar.Adapter {
	t.Helper()
	deps := sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(now)}
	if logs != nil {
		deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	a, err := legistar.New(legistar.OCTA, deps)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func octaPage(id int) string {
	return "https://octa.legistar.com/MeetingDetail.aspx?LEGID=" + strconv.Itoa(id) + "&GID=630&G=0491644B-C0F4-42D1-BF5E-4397E53988EE"
}

func octaAgenda(id int, day time.Time) string {
	return fmt.Sprintf("https://octa.legistar1.com/octa/meetings/%d/%d/%d_A_Board_%s_Agenda.pdf", day.Year(), day.Month(), id, day.Format("06-01-02"))
}

func octaClip(id int) string {
	return "https://octa.granicus.com/MediaPlayer.php?view_id=2&clip_id=" + strconv.Itoa(id)
}

func pacific(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func octaMeetings(t *testing.T, a *legistar.Adapter, window sources.Window) []sources.Meeting {
	t.Helper()
	got, err := a.Meetings(t.Context(), window)
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	return got
}

func wideWindow() sources.Window {
	return sources.Window{From: time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func recordedOCTAEvents(t *testing.T, name string) map[int]event {
	t.Helper()
	var events []event
	if err := json.Unmarshal(fixture(t, name), &events); err != nil {
		t.Fatal(err)
	}
	byID := map[int]event{}
	for _, e := range events {
		byID[int(e["EventId"].(float64))] = e
	}
	return byID
}

func TestOCTABoardMeetingsFromRecordedResponse(t *testing.T) {
	a := octa(t, serving(fixture(t, "octa_board_events.json")), recordedAt, nil)

	got := octaMeetings(t, a, wideWindow())

	held := func(id, clip int, day time.Time, startsAt time.Time) sources.Meeting {
		return sources.Meeting{
			SourceID:     strconv.Itoa(id),
			Title:        "Board",
			StartsAt:     startsAt,
			Status:       sources.StatusHeld,
			PageURL:      octaPage(id),
			AgendaURL:    octaAgenda(id, day),
			RecordingURL: octaClip(clip),
		}
	}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	want := []sources.Meeting{
		{
			SourceID:  "2900",
			Title:     "Board",
			StartsAt:  time.Date(2026, 8, 10, 16, 30, 0, 0, time.UTC),
			Status:    sources.StatusCancelled,
			PageURL:   octaPage(2900),
			AgendaURL: octaAgenda(2900, day(8, 10)),
		},
		held(2901, 3288, day(8, 24), time.Date(2026, 8, 24, 16, 30, 0, 0, time.UTC)),
		held(2902, 3297, day(9, 14), time.Date(2026, 9, 14, 16, 30, 0, 0, time.UTC)),
		held(2903, 3307, day(9, 28), time.Date(2026, 9, 28, 16, 30, 0, 0, time.UTC)),
		held(2904, 3314, day(10, 12), time.Date(2026, 10, 12, 16, 30, 0, 0, time.UTC)),
		held(2905, 3325, day(10, 26), time.Date(2026, 10, 26, 16, 30, 0, 0, time.UTC)),
		held(2906, 3339, day(11, 9), time.Date(2026, 11, 9, 17, 30, 0, 0, time.UTC)),
	}
	assertMeetings(t, got, want)
}

func TestOCTARecordedHistoryCoversCancelledSpecialWorkshopAndHiddenEvents(t *testing.T) {
	var logs bytes.Buffer
	a := octa(t, serving(fixture(t, "octa_board_history.json")), recordedAt, &logs)

	got := octaMeetings(t, a, wideWindow())

	type summary struct {
		startsAt  time.Time
		status    sources.Status
		recording string
	}
	want := map[string]summary{
		"1460": {time.Date(2016, 7, 11, 16, 0, 0, 0, time.UTC), sources.StatusCancelled, ""},
		"2198": {time.Date(2019, 5, 13, 7, 0, 0, 0, time.UTC), sources.StatusHeld, octaClip(1530)},
		"2444": {time.Date(2021, 11, 1, 16, 0, 0, 0, time.UTC), sources.StatusHeld, octaClip(2125)},
		"2805": {time.Date(2025, 3, 24, 16, 30, 0, 0, time.UTC), sources.StatusHeld, octaClip(2961)},
		"2823": {time.Date(2025, 11, 10, 17, 30, 0, 0, time.UTC), sources.StatusCancelled, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d meetings, want %d: %+v", len(got), len(want), got)
	}
	for _, m := range got {
		w, ok := want[m.SourceID]
		if !ok {
			t.Errorf("unexpected meeting %+v", m)
			continue
		}
		if !m.StartsAt.Equal(w.startsAt) || m.Status != w.status || m.RecordingURL != w.recording {
			t.Errorf("meeting %s = %+v, want %+v", m.SourceID, m, w)
		}
	}
	if n := strings.Count(logs.String(), "dropped a hidden board event"); n != 3 {
		t.Errorf("logged %d hidden events, want 3:\n%s", n, logs.String())
	}
	for _, id := range []string{"2536", "2543", "2665"} {
		if !strings.Contains(logs.String(), "event_id="+id) {
			t.Errorf("hidden event %s was not logged:\n%s", id, logs.String())
		}
	}
}

func TestOCTACommitteesInTheRecordedResponseAreDropped(t *testing.T) {
	var logs bytes.Buffer
	a := octa(t, serving(fixture(t, "octa_events_with_committees.json")), recordedAt, &logs)

	if got := octaMeetings(t, a, wideWindow()); len(got) != 0 {
		t.Errorf("meetings = %+v, want none: the response holds only committees", got)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("committee events were logged as unreadable:\n%s", logs.String())
	}
}

func TestOCTAQueryAsksOnlyForTheBoardBody(t *testing.T) {
	fake := serving([]byte("[]"))
	octaMeetings(t, octa(t, fake, recordedAt, nil), checkWindow(recordedAt))
	want := "https://webapi.legistar.com/v1/octa/events?%24filter=EventDate+ge+datetime%272026-10-18%27+and+EventDate+le+datetime%272027-02-15%27+and+%28EventBodyId+eq+138%29&%24orderby=EventDate%2CEventId&%24skip=0&%24top=1000"
	if calls := fake.Calls(); len(calls) != 1 || calls[0].URL != want {
		t.Errorf("requests = %+v, want one GET %s", calls, want)
	}
}

type feedState struct {
	name   string
	now    time.Time
	events []event
}

func octaFeedStates(t *testing.T) []feedState {
	t.Helper()
	recorded := recordedOCTAEvents(t, "octa_board_events.json")
	oct26, nov9 := recorded[2905], recorded[2906]
	upcoming := nov9.with("EventMedia", nil).
		with("EventMinutesStatusName", "DRAFT").
		with("EventAgendaLastPublishedUTC", "2026-11-05T18:02:11.4")
	revised := upcoming.with("EventComment", "REVISED").
		with("EventAgendaStatusName", "FINAL-REVISED").
		with("EventAgendaLastPublishedUTC", "2026-11-06T23:19:50.827")
	nov23 := nov9.with("EventId", 2907).
		with("EventGuid", "00000000-0000-0000-0000-000000002907").
		with("EventDate", "2026-11-23T00:00:00").
		with("EventMedia", nil).
		with("EventMinutesStatusName", "DRAFT").
		with("EventAgendaFile", nil).
		with("EventInSiteURL", octaPage(2907))
	nov23Agenda := nov23.with("EventAgendaFile", octaAgenda(2907, time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)))
	return []feedState{
		{"agenda posted before the meeting", time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC), []event{oct26, upcoming}},
		{"agenda revised, newest first", time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC), []event{revised, oct26}},
		{"the morning of the meeting", time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC), []event{revised, oct26}},
		{"archived with a recording", recordedAt, []event{oct26, nov9}},
		{"next meeting listed first without an agenda", time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC), []event{nov23, nov9, oct26}},
		{"next meeting's agenda posted", time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC), []event{nov9, nov23Agenda, oct26}},
	}
}

func TestOCTASourceIDsAreStableAcrossDailyRuns(t *testing.T) {
	wantStart := map[string]time.Time{
		"2905": time.Date(2026, 10, 26, 16, 30, 0, 0, time.UTC),
		"2906": time.Date(2026, 11, 9, 17, 30, 0, 0, time.UTC),
		"2907": time.Date(2026, 11, 23, 17, 30, 0, 0, time.UTC),
	}
	firstSeen := map[time.Time]string{}
	for _, state := range octaFeedStates(t) {
		t.Run(state.name, func(t *testing.T) {
			got := octaMeetings(t, octa(t, serving(encode(t, state.events...)), state.now, nil), checkWindow(state.now))
			if len(got) != len(state.events) {
				t.Fatalf("got %d meetings, want %d", len(got), len(state.events))
			}
			for _, m := range got {
				start, ok := wantStart[m.SourceID]
				if !ok || !m.StartsAt.Equal(start) {
					t.Errorf("source id %s maps to a meeting at %s, want %s", m.SourceID, m.StartsAt, start)
				}
				if id, seen := firstSeen[m.StartsAt]; seen && id != m.SourceID {
					t.Errorf("meeting at %s changed source id from %s to %s", m.StartsAt, id, m.SourceID)
				}
				firstSeen[m.StartsAt] = m.SourceID
			}
		})
	}
}

func TestOCTAUpcomingMeetingIsListedWithItsAgendaAndNoRecording(t *testing.T) {
	states := octaFeedStates(t)
	tests := []struct {
		state      int
		status     sources.Status
		recording  string
		withAgenda bool
	}{
		{0, sources.StatusScheduled, "", true},
		{1, sources.StatusScheduled, "", true},
		{2, sources.StatusScheduled, "", true},
		{3, sources.StatusHeld, octaClip(3339), true},
	}
	for _, tc := range tests {
		state := states[tc.state]
		t.Run(state.name, func(t *testing.T) {
			got := octaMeetings(t, octa(t, serving(encode(t, state.events...)), state.now, nil), checkWindow(state.now))
			i := slices.IndexFunc(got, func(m sources.Meeting) bool { return m.SourceID == "2906" })
			if i < 0 {
				t.Fatalf("meeting 2906 missing from %+v", got)
			}
			m := got[i]
			if m.Status != tc.status || m.RecordingURL != tc.recording || (m.AgendaURL != "") != tc.withAgenda {
				t.Errorf("meeting = %+v, want status %s, recording %q, agenda %v", m, tc.status, tc.recording, tc.withAgenda)
			}
		})
	}
}

func TestRecordingURLNeedsALinkedClipAndAMeetingThatHasStarted(t *testing.T) {
	nov9 := recordedOCTAEvents(t, "octa_board_events.json")[2906]
	tests := []struct {
		name string
		now  time.Time
		e    event
		want string
	}{
		{"past with a linked clip", recordedAt, nov9, octaClip(3339)},
		{"past without a clip", recordedAt, nov9.with("EventMedia", nil), ""},
		{"clip linked before the meeting", time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), nov9, ""},
		{"clip linked, an hour before the start", time.Date(2026, 11, 9, 16, 30, 0, 0, time.UTC), nov9, ""},
		{"at the start", time.Date(2026, 11, 9, 17, 30, 0, 0, time.UTC), nov9, octaClip(3339)},
		{"cancelled with a clip", recordedAt, nov9.with("EventComment", "CANCELLED"), ""},
		{"video path before the meeting", time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), nov9.with("EventVideoPath", "https://video.example.org/1"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := octaMeetings(t, octa(t, serving(encode(t, tc.e)), tc.now, nil), wideWindow())
			if len(got) != 1 || got[0].RecordingURL != tc.want {
				t.Errorf("meetings = %+v, want one with recording %q", got, tc.want)
			}
		})
	}
}

func TestOCTAStatus(t *testing.T) {
	nov9 := recordedOCTAEvents(t, "octa_board_events.json")[2906]
	tests := []struct {
		name string
		e    event
		want sources.Status
	}{
		{"recorded", nov9, sources.StatusHeld},
		{"final minutes status alone is not evidence", nov9.with("EventMedia", nil), sources.StatusScheduled},
		{"minutes file", nov9.with("EventMedia", nil).with("EventMinutesFile", "https://octa.legistar1.com/octa/meetings/2026/11/m.pdf"), sources.StatusHeld},
		{"cancelled in the comment only", nov9.with("EventComment", "CANCELLED"), sources.StatusCancelled},
		{"cancelled in the agenda status", nov9.with("EventAgendaStatusName", "CANCELLED"), sources.StatusCancelled},
		{"revised is not cancelled", nov9.with("EventComment", "REVISED"), sources.StatusHeld},
		{"special is not cancelled", nov9.with("EventComment", "SPECIAL MEETING"), sources.StatusHeld},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := octaMeetings(t, octa(t, serving(encode(t, tc.e)), recordedAt, nil), wideWindow())
			if len(got) != 1 || got[0].Status != tc.want {
				t.Errorf("meetings = %+v, want one %s", got, tc.want)
			}
		})
	}
}

func TestOCTAStartTimes(t *testing.T) {
	nov9 := recordedOCTAEvents(t, "octa_board_events.json")[2906].with("EventAgendaFile", nil)
	loc := pacific(t)
	tests := []struct {
		name  string
		date  string
		clock any
		want  time.Time
	}{
		{"standard time", "2026-11-09", "9:30 AM", time.Date(2026, 11, 9, 9, 30, 0, 0, loc)},
		{"daylight time", "2026-10-26", "9:30 AM", time.Date(2026, 10, 26, 9, 30, 0, 0, loc)},
		{"monday after daylight time starts", "2026-03-09", "9:30 AM", time.Date(2026, 3, 9, 16, 30, 0, 0, time.UTC)},
		{"monday after daylight time ends", "2026-11-02", "9:30 AM", time.Date(2026, 11, 2, 17, 30, 0, 0, time.UTC)},
		{"recorded double space and leading zero", "2016-07-11", "09:00  AM", time.Date(2016, 7, 11, 16, 0, 0, 0, time.UTC)},
		{"recorded null time of a workshop", "2019-05-13", nil, time.Date(2019, 5, 13, 7, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := nov9.with("EventDate", tc.date+"T00:00:00").with("EventTime", tc.clock)
			got := octaMeetings(t, octa(t, serving(encode(t, e)), recordedAt, nil), wideWindow())
			if len(got) != 1 || !got[0].StartsAt.Equal(tc.want) || got[0].StartsAt.Location() != time.UTC {
				t.Errorf("meetings = %+v, want one starting %s", got, tc.want.UTC())
			}
		})
	}
}

func TestAnUnreadableTimeIsLoggedAndFallsBackToLocalMidnight(t *testing.T) {
	var logs bytes.Buffer
	e := recordedOCTAEvents(t, "octa_board_events.json")[2906].with("EventTime", "TBD")
	got := octaMeetings(t, octa(t, serving(encode(t, e)), recordedAt, &logs), wideWindow())
	if len(got) != 1 || !got[0].StartsAt.Equal(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("meetings = %+v, want one at local midnight", got)
	}
	if !strings.Contains(logs.String(), "unreadable event time") {
		t.Errorf("unreadable time was not logged:\n%s", logs.String())
	}
}

func TestAnAgendaFileDatedDifferentlyFromTheEventIsLoggedAndTheEventDateWins(t *testing.T) {
	recorded := recordedOCTAEvents(t, "octa_board_events.json")
	tests := []struct {
		name   string
		e      event
		logged bool
	}{
		{"recorded agenda matches", recorded[2906], false},
		{"rescheduled after the agenda was named", recorded[2906].with("EventDate", "2026-11-10T00:00:00"), true},
		{"agenda without a date in its name", recorded[2906].with("EventAgendaFile", "https://octa.legistar1.com/octa/meetings/2026/11/agenda.pdf"), false},
		{"agenda name with an impossible date", recorded[2906].with("EventAgendaFile", "https://octa.legistar1.com/octa/meetings/2026/11/2906_A_Board_26-13-45_Agenda.pdf"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			got := octaMeetings(t, octa(t, serving(encode(t, tc.e)), recordedAt, &logs), wideWindow())
			date, _ := time.Parse("2006-01-02T15:04:05", tc.e["EventDate"].(string))
			want := time.Date(date.Year(), date.Month(), date.Day(), 9, 30, 0, 0, pacific(t))
			if len(got) != 1 || !got[0].StartsAt.Equal(want) {
				t.Errorf("meetings = %+v, want one starting %s", got, want)
			}
			if logged := strings.Contains(logs.String(), "different date"); logged != tc.logged {
				t.Errorf("logged mismatch = %v, want %v:\n%s", logged, tc.logged, logs.String())
			}
		})
	}
}

func TestOneUnreadableBoardEventIsSkippedAndLogged(t *testing.T) {
	recorded := recordedOCTAEvents(t, "octa_board_events.json")
	good := recorded[2906]
	tests := []struct {
		name string
		bad  any
	}{
		{"unparseable date", recorded[2905].with("EventDate", "26/10/2026")},
		{"no meeting page", recorded[2905].with("EventInSiteURL", nil)},
		{"wrong field type", json.RawMessage(`{"EventId":"2905","EventBodyId":138}`)},
		{"no event id", recorded[2905].with("EventId", 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal([]any{good, tc.bad})
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			got := octaMeetings(t, octa(t, serving(body), recordedAt, &logs), wideWindow())
			if len(got) != 1 || got[0].SourceID != "2906" {
				t.Errorf("meetings = %+v, want only 2906", got)
			}
			if !strings.Contains(logs.String(), `level=WARN msg="legistar: skipped an`) {
				t.Errorf("skipped event was not logged:\n%s", logs.String())
			}
		})
	}
}

func TestAllBoardEventsUnreadableIsAPermanentMalformedError(t *testing.T) {
	recorded := recordedOCTAEvents(t, "octa_board_events.json")
	body := encode(t, recorded[2905].with("EventDate", "2026-10-26"), recorded[2906].with("EventInSiteURL", ""))
	_, err := octa(t, serving(body), recordedAt, nil).Meetings(t.Context(), wideWindow())
	if !errors.Is(err, legistar.ErrMalformed) || sources.Retryable(err) {
		t.Errorf("err = %v, want a permanent ErrMalformed", err)
	}
}

func TestHiddenAndNonBoardEventsDoNotCountAsUnreadable(t *testing.T) {
	recorded := recordedOCTAEvents(t, "octa_board_events.json")
	hidden := recorded[2906].with("EventAgendaStatusName", "Hidden").with("EventInSiteURL", nil)
	other := recorded[2906].with("EventId", 9001).with("EventBodyId", 999).with("EventBodyName", "Special Board Workshop").with("EventDate", "bad")
	var logs bytes.Buffer
	got := octaMeetings(t, octa(t, serving(encode(t, hidden, other)), recordedAt, &logs), wideWindow())
	if len(got) != 0 {
		t.Errorf("meetings = %+v, want none", got)
	}
	for _, want := range []string{"dropped a hidden board event", "dropped an event of a body that is not a configured board", "body=\"Special Board Workshop\""} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
	}
}

func TestAFullPageWithoutReadableIDsIsMalformedAndSaysSo(t *testing.T) {
	for _, body := range []string{`[{"EventId":"x"}]`, `[{"EventId":0,"EventBodyId":138}]`} {
		t.Run(body, func(t *testing.T) {
			cfg := legistar.OCTA
			cfg.PageSize = 1
			a, err := legistar.New(cfg, sources.Deps{Fetcher: serving([]byte(body)), Clock: sourcestest.NewClock(recordedAt)})
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Meetings(t.Context(), wideWindow())
			if !errors.Is(err, legistar.ErrMalformed) || !strings.Contains(err.Error(), "page at offset 0 has no event with a readable id") {
				t.Errorf("err = %v, want ErrMalformed for a page without readable ids", err)
			}
		})
	}
}

func TestAResponseInWhichNoEventDecodesIsMalformed(t *testing.T) {
	_, err := octa(t, serving([]byte(`[{"EventId":"x"},{"EventDate":7}]`)), recordedAt, nil).Meetings(t.Context(), wideWindow())
	if !errors.Is(err, legistar.ErrMalformed) || !strings.Contains(err.Error(), "none of the 2 events in the window decode") {
		t.Errorf("err = %v, want ErrMalformed saying no event decodes", err)
	}
}

func TestHiddenEventsAndOneUndecodableItemAreNotMalformed(t *testing.T) {
	hidden := recordedOCTAEvents(t, "octa_board_history.json")[2536]
	body, err := json.Marshal([]any{hidden, json.RawMessage(`{"EventId":"2907"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	got := octaMeetings(t, octa(t, serving(body), recordedAt, &logs), wideWindow())
	if len(got) != 0 {
		t.Errorf("meetings = %+v, want none", got)
	}
	if !strings.Contains(logs.String(), "level=WARN msg=\"legistar: skipped an event that does not decode\"") {
		t.Errorf("undecodable item was not logged as a warning:\n%s", logs.String())
	}
}

func TestOCTACancellationFromEveryRecordedCommentValue(t *testing.T) {
	nov9 := recordedOCTAEvents(t, "octa_board_events.json")[2906]
	tests := []struct {
		comment string
		want    sources.Status
	}{
		{"CANCELLED", sources.StatusCancelled},
		{"REVISED", sources.StatusHeld},
		{"Revised", sources.StatusHeld},
		{"SPECIAL", sources.StatusHeld},
		{"SPECIAL MEETING", sources.StatusHeld},
		{"For Training Only", sources.StatusHeld},
		{"GRANICUS TESTING", sources.StatusHeld},
		{"Jordan's Agenda for Training", sources.StatusHeld},
		{" canceled ", sources.StatusCancelled},
		{"Cancelled - rescheduled to November 16", sources.StatusCancelled},
		{"Item 5 cancelled", sources.StatusHeld},
	}
	for _, tc := range tests {
		t.Run(tc.comment, func(t *testing.T) {
			got := octaMeetings(t, octa(t, serving(encode(t, nov9.with("EventComment", tc.comment))), recordedAt, nil), wideWindow())
			if len(got) != 1 || got[0].Status != tc.want {
				t.Errorf("meetings = %+v, want one %s", got, tc.want)
			}
		})
	}
}

func TestLAMetroCommentsNeverCancelAMeeting(t *testing.T) {
	prose := "Members of the public may address the Board on agenda items.\r\n\r\n" +
		"Closed Session item 3 has been cancelled.\r\n\r\n" +
		"Live public comment can be given by telephone at 202.735.3323, English access code 5647249#."
	past := boardEvent(1, "2026-10-22", "10:00 AM").with("EventMedia", "4309")
	for _, comment := range []string{prose, "CANCELLED"} {
		t.Run(comment[:9], func(t *testing.T) {
			m := onlyMeeting(t, past.with("EventComment", comment))
			if m.Status != sources.StatusHeld || m.RecordingURL != metroClip("4309") {
				t.Errorf("meeting = %+v, want held with its recording", m)
			}
		})
	}
}
