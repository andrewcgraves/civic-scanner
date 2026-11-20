package primegov_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/primegov"
)

type doc struct {
	ID          int    `json:"id"`
	TemplateID  int    `json:"templateId"`
	Name        string `json:"templateName"`
	OutputType  int    `json:"compileOutputType"`
	PublishDate string `json:"publishDate"`
}

type mtg struct {
	ID          int    `json:"id"`
	TypeID      int    `json:"meetingTypeId"`
	CommitteeID int    `json:"committeeId"`
	DateTime    string `json:"dateTime"`
	Date        string `json:"date"`
	Time        string `json:"time"`
	Documents   []doc  `json:"documentList"`
	VideoURL    any    `json:"videoUrl"`
	Title       string `json:"title"`
}

func boardMeeting(id int, dateTime string, docs ...doc) mtg {
	day, _ := time.Parse("2006-01-02T15:04:05", dateTime)
	return mtg{
		ID: id, TypeID: 2, CommitteeID: 1, DateTime: dateTime,
		Date: day.Format("Jan 02, 2006"), Time: day.Format("03:04 PM"),
		Documents: append([]doc{}, docs...), Title: "Board of Directors",
	}
}

func (m mtg) titled(title string) mtg {
	m.Title = title
	return m
}

func (m mtg) in(committee int) mtg {
	m.CommitteeID = committee
	return m
}

func packet(templateID int, published string) doc {
	return doc{ID: templateID + 1000, TemplateID: templateID, Name: "Packet", OutputType: 1, PublishDate: published}
}

func named(name string, templateID int) doc {
	return doc{ID: templateID + 1000, TemplateID: templateID, Name: name, OutputType: 1, PublishDate: "2026-11-01T10:00:00.5"}
}

func list(t *testing.T, ms ...mtg) []byte {
	t.Helper()
	body, err := json.Marshal(append([]mtg{}, ms...))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

var rulesNow = time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)

func run(t *testing.T, now time.Time, window sources.Window, upcoming []mtg, archived ...mtg) ([]sources.Meeting, string) {
	t.Helper()
	a, logs := logged(t, primegov.SBCTA, serving(list(t, upcoming...), map[int][]byte{now.Year(): list(t, archived...)}), now)
	got, err := a.Meetings(t.Context(), window)
	if err != nil {
		t.Fatalf("Meetings: %v\n%s", err, logs)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	return got, logs.String()
}

func one(t *testing.T, got []sources.Meeting) sources.Meeting {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("got %d meetings, want 1: %+v", len(got), got)
	}
	return got[0]
}

func TestOnlyTheFullBoardIsKept(t *testing.T) {
	cases := []struct {
		name string
		m    mtg
		keep bool
		log  string
	}{
		{"regular", boardMeeting(1, "2026-12-02T10:00:00"), true, ""},
		{"special, leading", boardMeeting(1, "2026-12-02T10:00:00").titled("Special Board of Directors"), true, ""},
		{"special meeting of the board", boardMeeting(1, "2026-12-02T10:00:00").titled("Special Meeting of the Board of Directors"), true, ""},
		{"special, trailing", boardMeeting(1, "2026-12-02T10:00:00").titled("Board of Directors - Special Meeting"), true, ""},
		{"special, in parentheses", boardMeeting(1, "2026-12-02T10:00:00").titled("Board of Directors (Special Meeting)"), true, ""},
		{"case and spacing", boardMeeting(1, "2026-12-02T10:00:00").titled("  BOARD   of directors "), true, ""},
		{"entities", boardMeeting(1, "2026-12-02T10:00:00").titled("Board&#32;of Directors"), true, ""},
		{"test meeting under the board", boardMeeting(1, "2026-12-02T10:00:00").titled("TEST Board PLEASE IGNORE"), false, "level=WARN msg=\"primegov: dropped a meeting of the board's committee whose title is not the board's\""},
		{"workshop under the board", boardMeeting(1, "2026-12-02T10:00:00").titled("Board of Directors Workshop"), false, "level=WARN"},
		{"study session", boardMeeting(1, "2026-12-02T10:00:00").titled("Board of Directors Metro Valley Study Session").in(7), false, "level=INFO msg=\"primegov: dropped a meeting that names the board but is not of the board's committee\""},
		{"board title under a committee", boardMeeting(1, "2026-12-02T10:00:00").in(6), false, "level=INFO"},
		{"committee", boardMeeting(1, "2026-12-02T10:00:00").titled("Transit Committee Meeting ").in(6), false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, logs := run(t, rulesNow, checkWindow(rulesNow), []mtg{c.m, boardMeeting(99, "2027-01-06T10:00:00")})
			kept := len(got) == 2
			if kept != c.keep {
				t.Fatalf("kept = %v, want %v: %+v", kept, c.keep, got)
			}
			if c.log != "" && !strings.Contains(logs, c.log) {
				t.Errorf("logs lack %q:\n%s", c.log, logs)
			}
			if c.log == "" && strings.Contains(logs, "level=WARN") {
				t.Errorf("unexpected warning:\n%s", logs)
			}
		})
	}
}

func TestTitleKeepsSpecialAndDropsCancelMarkers(t *testing.T) {
	cases := map[string]struct {
		title, want string
		status      sources.Status
	}{
		"plain":               {"Board of Directors", "Board of Directors", sources.StatusScheduled},
		"special":             {"Special Board of Directors", "Special Board of Directors", sources.StatusScheduled},
		"dash cancelled":      {"Board of Directors - CANCELLED ", "Board of Directors", sources.StatusCancelled},
		"canceled in parens":  {"Board of Directors (Canceled)", "Board of Directors", sources.StatusCancelled},
		"leading cancelled":   {"CANCELLED: Board of Directors", "Board of Directors", sources.StatusCancelled},
		"dark":                {"Board of Directors (DARK)", "Board of Directors", sources.StatusCancelled},
		"special cancelled":   {"Special Board of Directors - CANCELLED", "Special Board of Directors", sources.StatusCancelled},
		"cancel word inside":  {"Board of Directors Cancelled Items Review", "", ""},
		"dark word is a name": {"Dark Sky Board of Directors", "", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, _ := run(t, rulesNow, checkWindow(rulesNow), []mtg{boardMeeting(1, "2026-12-02T10:00:00").titled(c.title)})
			if c.want == "" {
				if len(got) != 0 {
					t.Fatalf("kept %+v, want it dropped", got)
				}
				return
			}
			m := one(t, got)
			if m.Title != c.want || m.Status != c.status {
				t.Errorf("title %q status %s, want %q %s", m.Title, m.Status, c.want, c.status)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	past := "2026-11-04T10:00:00"
	future := "2026-12-02T10:00:00"
	cases := []struct {
		name string
		m    mtg
		want sources.Status
	}{
		{"past with a video link", boardMeeting(1, past, packet(645, "2026-10-29T21:08:37.74"), named("Video Link", 647)), sources.StatusHeld},
		{"past with a video url", func() mtg { m := boardMeeting(1, past); m.VideoURL = "https://vimeo.com/1"; return m }(), sources.StatusHeld},
		{"past with a packet only", boardMeeting(1, past, packet(645, "2026-10-29T21:08:37.74")), sources.StatusScheduled},
		{"past with an audio link", boardMeeting(1, past, named("Audio Link", 1)), sources.StatusScheduled},
		{"future with a video link", boardMeeting(1, future, named("Video Link", 1)), sources.StatusScheduled},
		{"cancellation notice", boardMeeting(1, future, named("Cancellation Notice", 743)), sources.StatusCancelled},
		{"cancellation notice after a packet", boardMeeting(1, future, packet(645, "2026-10-29T21:08:37.74"), named(" cancellation  notice ", 743)), sources.StatusCancelled},
		{"cancelled with a video link", boardMeeting(1, past, named("Video Link", 1)).titled("Board of Directors - CANCELLED"), sources.StatusCancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := run(t, rulesNow, checkWindow(rulesNow), []mtg{c.m})
			m := one(t, got)
			if m.Status != c.want || m.RecordingURL != "" {
				t.Errorf("status %s recording %q, want %s and no recording", m.Status, m.RecordingURL, c.want)
			}
		})
	}
}

func TestStartTimesAcrossDaylightSavingTime(t *testing.T) {
	cases := []struct {
		dateTime string
		want     time.Time
	}{
		{"2026-10-28T10:00:00", time.Date(2026, 10, 28, 17, 0, 0, 0, time.UTC)},
		{"2026-11-04T10:00:00", time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)},
		{"2027-03-10T10:00:00", time.Date(2027, 3, 10, 18, 0, 0, 0, time.UTC)},
		{"2027-03-17T10:00:00", time.Date(2027, 3, 17, 17, 0, 0, 0, time.UTC)},
		{"2026-11-01T01:30:00", time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)},
		{"2026-11-25T15:30:00.000", time.Date(2026, 11, 25, 23, 30, 0, 0, time.UTC)},
	}
	window := sources.Window{From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)}
	for _, c := range cases {
		m := boardMeeting(1, strings.TrimSuffix(c.dateTime, ".000"), packet(9, ""))
		m.DateTime = c.dateTime
		got, logs := run(t, rulesNow, window, []mtg{m})
		if s := one(t, got).StartsAt; !s.Equal(c.want) || s.Location() != time.UTC {
			t.Errorf("%s: starts at %s, want %s", c.dateTime, s, c.want)
		}
		if strings.Contains(logs, "level=WARN") {
			t.Errorf("%s: warnings:\n%s", c.dateTime, logs)
		}
	}
}

func TestMeetingWithoutAStatedTimeStartsAtTheBoardsUsualTime(t *testing.T) {
	m := boardMeeting(1, "2026-12-02T00:00:00")
	m.Time = ""
	got, logs := run(t, rulesNow, checkWindow(rulesNow), []mtg{m})
	if s := one(t, got).StartsAt; !s.Equal(time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("starts at %s, want 10:00 local", s)
	}
	if !strings.Contains(logs, `level=INFO msg="primegov: board meeting has no start time; using the configured start time"`) {
		t.Errorf("no info log:\n%s", logs)
	}

	midnight := boardMeeting(1, "2026-12-02T00:00:00")
	got, _ = run(t, rulesNow, checkWindow(rulesNow), []mtg{midnight})
	if s := one(t, got).StartsAt; !s.Equal(time.Date(2026, 12, 2, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("stated midnight starts at %s, want 00:00 local", s)
	}
}

func TestDateAndTimeFieldsWhenDateTimeIsMissing(t *testing.T) {
	cases := []struct {
		name       string
		date, time string
		want       time.Time
		log        string
	}{
		{"both", "Dec 02, 2026", "10:00 AM", time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC), ""},
		{"afternoon", "Dec 2, 2026", "3:30 pm", time.Date(2026, 12, 2, 23, 30, 0, 0, time.UTC), ""},
		{"no time", "Dec 02, 2026", "", time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC), "level=INFO"},
		{"unreadable time", "Dec 02, 2026", "morning", time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC), "level=WARN msg=\"primegov: board meeting has an unreadable start time; using the configured start time\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := boardMeeting(1, "2026-12-02T10:00:00")
			m.DateTime, m.Date, m.Time = "", c.date, c.time
			got, logs := run(t, rulesNow, checkWindow(rulesNow), []mtg{m, boardMeeting(2, "2027-01-06T10:00:00")})
			if len(got) != 2 || !got[0].StartsAt.Equal(c.want) {
				t.Fatalf("got %+v, want meeting 1 at %s", got, c.want)
			}
			if c.log != "" && !strings.Contains(logs, c.log) {
				t.Errorf("logs lack %q:\n%s", c.log, logs)
			}
		})
	}
}

func TestDisagreeingDateFieldsWarnAndKeepDateTime(t *testing.T) {
	m := boardMeeting(1, "2026-12-02T10:00:00")
	m.Date, m.Time = "Dec 03, 2026", "09:00 AM"
	got, logs := run(t, rulesNow, checkWindow(rulesNow), []mtg{m})
	if s := one(t, got).StartsAt; !s.Equal(time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("starts at %s, want dateTime", s)
	}
	for _, want := range []string{"names another day than its dateTime", "names another time than its dateTime"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}

func TestNoWarningsForOldOrFarItemsOutsideTheWindow(t *testing.T) {
	odd := boardMeeting(1, "2026-03-04T10:00:00")
	odd.Date, odd.Time = "Mar 05, 2026", "nonsense"
	future := boardMeeting(2, "2030-12-25T15:30:00").titled("TEST Board PLEASE IGNORE")
	stale := boardMeeting(3, "2026-02-04T00:00:00")
	stale.Time = ""
	got, logs := run(t, rulesNow, checkWindow(rulesNow), []mtg{future, boardMeeting(4, "2026-12-02T10:00:00")}, odd, stale)
	if len(got) != 1 || got[0].SourceID != "4" {
		t.Errorf("got %+v, want meeting 4 alone", got)
	}
	if strings.Contains(logs, "level=WARN") || strings.Contains(logs, "level=INFO") {
		t.Errorf("logs above debug for items outside the window:\n%s", logs)
	}
}

func TestAgendaIsTheNewestPublishedPacket(t *testing.T) {
	cases := []struct {
		name string
		docs []doc
		want string
	}{
		{"packet among others", []doc{named("Support Materials", 648), packet(645, "2026-10-29T21:08:37.74"), named("Supplemental Items", 650), named("Video Link", 647)}, packetURL + "645&compileOutputType=1"},
		{"newer packet wins", []doc{packet(700, "2026-10-29T21:08:37.74"), packet(645, "2026-10-30T08:00:00")}, packetURL + "645&compileOutputType=1"},
		{"same time, higher template", []doc{packet(645, "2026-10-29T21:08:37"), packet(700, "2026-10-29T21:08:37")}, packetURL + "700&compileOutputType=1"},
		{"name in another case", []doc{{TemplateID: 645, Name: " PACKET ", OutputType: 1}}, packetURL + "645&compileOutputType=1"},
		{"no output type", []doc{{TemplateID: 645, Name: "Packet"}}, packetURL + "645&compileOutputType=1"},
		{"another output type", []doc{{TemplateID: 645, Name: "Packet", OutputType: 3}}, packetURL + "645&compileOutputType=3"},
		{"no template id", []doc{{Name: "Packet", OutputType: 1}}, ""},
		{"revised items are not the agenda", []doc{named("REVISED ITEMS", 729)}, ""},
		{"cancellation notice is not the agenda", []doc{named("Cancellation Notice", 743)}, ""},
		{"none yet", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := run(t, rulesNow, checkWindow(rulesNow), []mtg{boardMeeting(1, "2026-12-02T10:00:00", c.docs...)})
			if a := one(t, got).AgendaURL; a != c.want {
				t.Errorf("agenda %q, want %q", a, c.want)
			}
		})
	}
}

func TestMissingAgendaWarnsOnlyWhenTheMeetingIsNearOrPast(t *testing.T) {
	cases := []struct {
		name string
		m    mtg
		warn bool
	}{
		{"weeks ahead", boardMeeting(1, "2026-12-16T10:00:00"), false},
		{"in two days", boardMeeting(1, "2026-11-21T10:00:00"), true},
		{"past", boardMeeting(1, "2026-11-04T10:00:00"), true},
		{"past and cancelled", boardMeeting(1, "2026-11-04T10:00:00").titled("Board of Directors - CANCELLED"), false},
		{"in two days with a packet", boardMeeting(1, "2026-11-21T10:00:00", packet(9, "")), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, logs := run(t, rulesNow, checkWindow(rulesNow), []mtg{c.m})
			warned := strings.Contains(logs, "no agenda document though the meeting is near or past")
			if warned != c.warn {
				t.Errorf("warned = %v, want %v:\n%s", warned, c.warn, logs)
			}
		})
	}
}

func TestOnlyMeetingsInTheWindowAreReturned(t *testing.T) {
	window := sources.Window{From: time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC), To: time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC)}
	got, _ := run(t, rulesNow, window,
		[]mtg{boardMeeting(3, "2026-12-02T10:00:00"), boardMeeting(4, "2026-12-02T10:01:00").titled("Special Board of Directors")},
		boardMeeting(1, "2026-11-04T09:59:00"), boardMeeting(2, "2026-11-04T10:00:00"))
	var ids []string
	for _, m := range got {
		ids = append(ids, m.SourceID)
	}
	if !reflect.DeepEqual(ids, []string{"2", "3"}) {
		t.Errorf("ids %v, want [2 3]: the window's ends are inclusive", ids)
	}
}

func TestArchivedYearsCoverTheLookBack(t *testing.T) {
	cases := []struct {
		name   string
		now    time.Time
		window sources.Window
		want   []string
	}{
		{"november", rulesNow, checkWindow(rulesNow), []string{upcomingURL, archivedURL + "2026"}},
		{"january", time.Date(2027, 1, 10, 13, 0, 0, 0, time.UTC), checkWindow(time.Date(2027, 1, 10, 13, 0, 0, 0, time.UTC)), []string{upcomingURL, archivedURL + "2026", archivedURL + "2027"}},
		{"new year in los angeles", time.Date(2027, 1, 1, 7, 59, 0, 0, time.UTC), sources.Window{From: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)}, []string{upcomingURL, archivedURL + "2026"}},
		{"future window", rulesNow, sources.Window{From: time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)}, []string{upcomingURL}},
		{"decades back", rulesNow, sources.Window{From: time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC), To: rulesNow}, []string{upcomingURL, archivedURL + "2022", archivedURL + "2023", archivedURL + "2024", archivedURL + "2025", archivedURL + "2026"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := serving(list(t, boardMeeting(1, "2027-06-02T10:00:00")), nil)
			if _, err := adapter(t, primegov.SBCTA, fake, c.now).Meetings(t.Context(), c.window); err != nil {
				t.Fatal(err)
			}
			if got := urls(fake.Calls()); !reflect.DeepEqual(got, c.want) {
				t.Errorf("requests %v, want %v", got, c.want)
			}
		})
	}
}

func TestMeetingInBothListsIsOneMeetingAndTheArchivedEntryWins(t *testing.T) {
	upcoming := boardMeeting(100, "2026-11-19T10:00:00", packet(645, "2026-10-29T21:08:37.74"))
	archived := boardMeeting(100, "2026-11-19T10:00:00", packet(645, "2026-10-29T21:08:37.74"), named("Video Link", 647))
	got, _ := run(t, time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC), checkWindow(rulesNow), []mtg{upcoming}, archived)
	if m := one(t, got); m.Status != sources.StatusHeld {
		t.Errorf("status %s, want held from the archived entry", m.Status)
	}
}

func TestTwoBoardMeetingsOnOneDayAreBothKeptWithAWarning(t *testing.T) {
	got, logs := run(t, rulesNow, checkWindow(rulesNow), []mtg{boardMeeting(1, "2026-12-02T10:00:00"), boardMeeting(2, "2026-12-02T13:00:00").titled("Special Board of Directors")})
	if len(got) != 2 {
		t.Fatalf("got %+v, want both", got)
	}
	if !strings.Contains(logs, `level=WARN msg="primegov: two board meetings on one day; both are stored" host=sbcta.primegov.com date=2026-12-02 meetings=1,2`) {
		t.Errorf("no duplicate warning:\n%s", logs)
	}
}

func TestSourceIDsAreStableAcrossDailyRuns(t *testing.T) {
	var recordedBoard mtg
	var all []mtg
	if err := json.Unmarshal(fixture(t, "sbcta_archived_2026.json"), &all); err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.ID == 100 {
			recordedBoard = m
		}
	}
	if recordedBoard.ID != 100 {
		t.Fatal("meeting 100 is not in the archived fixture")
	}
	scheduled := recordedBoard
	scheduled.Documents = nil
	posted := recordedBoard
	posted.Documents = recordedBoard.Documents[1:2]
	rescheduled := posted
	rescheduled.DateTime, rescheduled.Date, rescheduled.Time = "2026-11-05T13:30:00", "Nov 05, 2026", "01:30 PM"
	renamed := posted.titled("Special Board of Directors")
	cancelled := posted.titled("Board of Directors - CANCELLED")
	next := boardMeeting(101, "2026-12-02T10:00:00")

	states := []struct {
		name     string
		now      time.Time
		upcoming []mtg
		archived []mtg
		status   sources.Status
	}{
		{"calendar", time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC), []mtg{next, scheduled}, nil, sources.StatusScheduled},
		{"packet posted", time.Date(2026, 10, 30, 13, 0, 0, 0, time.UTC), []mtg{posted, next}, nil, sources.StatusScheduled},
		{"rescheduled", time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC), []mtg{next, rescheduled}, nil, sources.StatusScheduled},
		{"renamed", time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC), []mtg{renamed, next}, nil, sources.StatusScheduled},
		{"cancelled", time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC), []mtg{next, cancelled}, nil, sources.StatusCancelled},
		{"held", time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC), []mtg{next}, []mtg{recordedBoard}, sources.StatusHeld},
	}
	for _, s := range states {
		got, _ := run(t, s.now, checkWindow(s.now), s.upcoming, s.archived...)
		byID := map[string]sources.Meeting{}
		for _, m := range got {
			byID[m.SourceID] = m
		}
		if len(byID) != 2 || byID["101"].StartsAt != time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC) {
			t.Fatalf("%s: got %+v, want meetings 100 and 101", s.name, got)
		}
		if m := byID["100"]; m.Status != s.status {
			t.Errorf("%s: meeting 100 status %s, want %s", s.name, m.Status, s.status)
		}
	}
}

func TestRequestsGoThroughTheFetcherOnly(t *testing.T) {
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if !strings.HasPrefix(req.URL, "https://sbcta.primegov.com/api/v2/PublicPortal/") {
			t.Errorf("request to %s", req.URL)
		}
		return fetch.Response{Status: 200, Body: []byte("[]"), FinalURL: req.URL}, nil
	}}
	got, err := adapter(t, primegov.SBCTA, fake, rulesNow).Meetings(t.Context(), checkWindow(rulesNow))
	if err != nil || len(got) != 0 {
		t.Errorf("empty lists: %+v, %v; want no meetings and no error", got, err)
	}
}
