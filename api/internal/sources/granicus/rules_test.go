package granicus_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/granicus"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

type entry struct {
	title     string
	link      string
	parts     string
	enclosure string
}

func parts(local string) string {
	t, err := time.Parse("2006-01-02 15:04", local)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("yr='%d' mo='%02d' day='%02d' hr='%02d' min='%02d' sec='0' tz='8'", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
}

func titleOn(name, local string) string {
	t, err := time.Parse("2006-01-02 15:04", local)
	if err != nil {
		panic(err)
	}
	return name + " - " + t.Format("Jan 02, 2006")
}

func clipItem(clip int, local string) entry {
	return entry{title: titleOn("Board of Directors", local), link: clipAgenda(clip), parts: parts(local)}
}

func videoItem(clip int, local string) entry {
	e := clipItem(clip, local)
	e.link = mediaPlayer(clip)
	e.enclosure = fmt.Sprintf("https://metrolink.granicus.com/DownloadFile.php?view_id=8&clip_id=%d", clip)
	return e
}

func eventItem(event int, meetingDay, published string) entry {
	return entry{title: titleOn("Board of Directors", meetingDay+" 00:00"), link: eventAgenda(event), parts: parts(published)}
}

func (e entry) titled(title string) entry {
	e.title = title
	return e
}

func rss(entries ...entry) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" ?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:gran="https://www.granicus.com/schema/rss-supplements"><channel><title>Feed</title>`)
	for _, e := range entries {
		fmt.Fprintf(&b, "<item><title>%s</title><pubDate>Fri, 11 Dec 2026 10:00:00 -0800</pubDate>", e.title)
		if e.parts != "" {
			fmt.Fprintf(&b, "<gran:pubDateParts %s />", e.parts)
		}
		fmt.Fprintf(&b, "<link>%s</link>", strings.ReplaceAll(e.link, "&", "&amp;"))
		if e.enclosure != "" {
			fmt.Fprintf(&b, `<enclosure url='%s' type="video/x-ms-wmv" />`, strings.ReplaceAll(e.enclosure, "&", "&amp;"))
		}
		b.WriteString("</item>")
	}
	b.WriteString("</channel></rss>")
	return []byte(b.String())
}

func meetingsFrom(t *testing.T, agendas, vod []entry) []sources.Meeting {
	t.Helper()
	got, err := metrolink(t, serving(rss(agendas...), rss(vod...))).Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestUpcomingEventIsScheduledAtTheDefaultStartTime(t *testing.T) {
	got := meetingsFrom(t, []entry{eventItem(612, "2026-12-11", "2026-12-04 15:57")}, nil)
	assertMeetings(t, got, []sources.Meeting{{
		SourceID:  "board-of-directors/2026-12-11",
		Title:     "Board of Directors",
		StartsAt:  time.Date(2026, 12, 11, 18, 0, 0, 0, time.UTC),
		Status:    sources.StatusScheduled,
		PageURL:   eventAgenda(612),
		AgendaURL: eventAgenda(612),
	}})
}

func TestEventKeepsItsSourceIDWhenItBecomesAClip(t *testing.T) {
	before := meetingsFrom(t, []entry{eventItem(612, "2026-11-13", "2026-11-06 15:57")}, nil)
	after := meetingsFrom(t, []entry{clipItem(1438, "2026-11-13 10:15")}, []entry{videoItem(1438, "2026-11-13 10:15")})
	if len(before) != 1 || len(after) != 1 || before[0].SourceID != after[0].SourceID {
		t.Fatalf("before = %+v\nafter = %+v\nwant one meeting with the same source id", before, after)
	}
	if !after[0].StartsAt.Equal(time.Date(2026, 11, 13, 18, 15, 0, 0, time.UTC)) || after[0].Status != sources.StatusHeld {
		t.Errorf("archived meeting = %+v, want the clip's start time and held", after[0])
	}
}

func TestEventAndItsClipInOneReadAreOneMeeting(t *testing.T) {
	got := meetingsFrom(t,
		[]entry{eventItem(612, "2026-11-13", "2026-11-06 15:57"), clipItem(1438, "2026-11-13 10:00")},
		[]entry{videoItem(1438, "2026-11-13 10:00")})
	assertMeetings(t, got, []sources.Meeting{heldBoardMeeting(1438, time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC))})
}

func onlyMeeting(t *testing.T, agendas, vod []entry) sources.Meeting {
	t.Helper()
	got := meetingsFrom(t, agendas, vod)
	if len(got) != 1 {
		t.Fatalf("meetings = %+v, want exactly one", got)
	}
	return got[0]
}

func TestOneBoardOnOneDayIsOneMeetingAcrossRuns(t *testing.T) {
	const id = "board-of-directors/2026-11-13"
	revised := eventItem(615, "2026-11-13", "2026-11-11 16:00").titled("Board of Directors - REVISED AGENDA - Nov 13, 2026")
	corrected := clipItem(1439, "2026-11-13 14:00").titled("Board of Directors - CORRECTED AGENDA - Nov 13, 2026")
	correctedVideo := videoItem(1439, "2026-11-13 14:00").titled("Board of Directors - CORRECTED AGENDA - Nov 13, 2026")
	runs := []struct {
		name         string
		agendas, vod []entry
		want         sources.Meeting
	}{
		{
			name:    "upcoming event",
			agendas: []entry{eventItem(612, "2026-11-13", "2026-11-06 15:57")},
			want: sources.Meeting{
				SourceID: id, Title: "Board of Directors", StartsAt: time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC),
				Status: sources.StatusScheduled, PageURL: eventAgenda(612), AgendaURL: eventAgenda(612),
			},
		},
		{
			name:    "revised agenda as a second event",
			agendas: []entry{revised, eventItem(612, "2026-11-13", "2026-11-06 15:57")},
			want: sources.Meeting{
				SourceID: id, Title: "Board of Directors", StartsAt: time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC),
				Status: sources.StatusScheduled, PageURL: eventAgenda(615), AgendaURL: eventAgenda(615),
			},
		},
		{
			name:    "archived clip next to the events",
			agendas: []entry{revised, clipItem(1438, "2026-11-13 10:15"), eventItem(612, "2026-11-13", "2026-11-06 15:57")},
			vod:     []entry{videoItem(1438, "2026-11-13 10:15")},
			want: sources.Meeting{
				SourceID: id, Title: "Board of Directors", StartsAt: time.Date(2026, 11, 13, 18, 15, 0, 0, time.UTC),
				Status: sources.StatusHeld, PageURL: mediaPlayer(1438), AgendaURL: clipAgenda(1438), RecordingURL: mediaPlayer(1438),
			},
		},
		{
			name:    "corrected agenda clip with a different start",
			agendas: []entry{corrected, clipItem(1438, "2026-11-13 10:15")},
			vod:     []entry{correctedVideo, videoItem(1438, "2026-11-13 10:15")},
			want: sources.Meeting{
				SourceID: id, Title: "Board of Directors", StartsAt: time.Date(2026, 11, 13, 18, 15, 0, 0, time.UTC),
				Status: sources.StatusHeld, PageURL: mediaPlayer(1438), AgendaURL: clipAgenda(1439), RecordingURL: mediaPlayer(1438),
			},
		},
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			assertMeetings(t, []sources.Meeting{onlyMeeting(t, run.agendas, run.vod)}, []sources.Meeting{run.want})
		})
	}
}

func TestSameNamedItemsOnOneDayCollapseInEveryArrivalOrder(t *testing.T) {
	agendas := []entry{
		clipItem(1449, "2026-11-13 09:00"),
		clipItem(1450, "2026-11-13 14:00"),
		eventItem(612, "2026-11-13", "2026-11-06 15:57"),
	}
	videos := []entry{videoItem(1449, "2026-11-13 09:00"), videoItem(1450, "2026-11-13 14:00")}
	want := sources.Meeting{
		SourceID: "board-of-directors/2026-11-13", Title: "Board of Directors", StartsAt: time.Date(2026, 11, 13, 17, 0, 0, 0, time.UTC),
		Status: sources.StatusHeld, PageURL: mediaPlayer(1449), AgendaURL: clipAgenda(1450), RecordingURL: mediaPlayer(1449),
	}
	orders := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, order := range orders {
		for _, reverseVideo := range []bool{false, true} {
			t.Run(fmt.Sprint(order, reverseVideo), func(t *testing.T) {
				var a []entry
				for _, i := range order {
					a = append(a, agendas[i])
				}
				v := []entry{videos[0], videos[1]}
				if reverseVideo {
					v[0], v[1] = v[1], v[0]
				}
				assertMeetings(t, []sources.Meeting{onlyMeeting(t, a, v)}, []sources.Meeting{want})
			})
		}
	}
	for _, partial := range [][]entry{{agendas[0]}, {agendas[1]}, {agendas[2]}, {agendas[1], agendas[2]}} {
		if got := onlyMeeting(t, partial, nil); got.SourceID != want.SourceID {
			t.Errorf("source id = %q from %+v, want %q", got.SourceID, partial, want.SourceID)
		}
	}
}

func TestClipsOfOneMeetingWithDifferentStartsAreLogged(t *testing.T) {
	var logs bytes.Buffer
	a, err := granicus.New(granicus.Metrolink, sources.Deps{
		Fetcher: serving(rss(clipItem(1449, "2026-11-13 09:00"), clipItem(1450, "2026-11-13 14:00")), rss()),
		Clock:   sourcestest.NewClock(recordedAt),
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(logs.String(), "different start times") ||
		!strings.Contains(logs.String(), "1449") || !strings.Contains(logs.String(), "1450") {
		t.Errorf("meetings = %+v, logs = %s, want one meeting and a warning naming both clips", got, logs.String())
	}
}

func TestClipsOfOneMeetingWithTheSameStartAreNotLogged(t *testing.T) {
	var logs bytes.Buffer
	a, err := granicus.New(granicus.Metrolink, sources.Deps{
		Fetcher: serving(rss(clipItem(1438, "2026-11-13 10:00"), clipItem(1439, "2026-11-13 10:00")), rss()),
		Clock:   sourcestest.NewClock(recordedAt),
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AgendaURL != clipAgenda(1439) || logs.Len() != 0 {
		t.Errorf("meetings = %+v, logs = %s, want one meeting with the later agenda and no warning", got, logs.String())
	}
}

func TestDistinctBoardNamesOnOneDayStayDistinct(t *testing.T) {
	special := eventItem(613, "2026-11-13", "2026-11-06 15:57").titled("Special Board of Directors - Nov 13, 2026")
	got := meetingsFrom(t, []entry{eventItem(612, "2026-11-13", "2026-11-06 15:57"), special}, nil)
	if len(got) != 2 || got[0].SourceID != "board-of-directors/2026-11-13" || got[1].SourceID != "special-board-of-directors/2026-11-13" {
		t.Errorf("meetings = %+v, want one per board name", got)
	}
}

func TestClipWhoseDatePartsAreOnAnotherDayStartsAtTheDefaultTime(t *testing.T) {
	item := clipItem(1304, "2025-04-01 09:15").titled("Board of Directors - Mar 28, 2025")
	window := sources.Window{From: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)}
	got, err := metrolink(t, serving(rss(item), rss())).Meetings(t.Context(), window)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "board-of-directors/2025-03-28" || !got[0].StartsAt.Equal(time.Date(2025, 3, 28, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("meetings = %+v, want 10:00 local on the title's date", got)
	}
}

func TestRepeatedItemsCountOnce(t *testing.T) {
	agenda := clipItem(1436, "2026-10-23 10:15")
	video := videoItem(1436, "2026-10-23 10:15")
	event := eventItem(700, "2026-12-11", "2026-12-04 09:00")
	got := meetingsFrom(t, []entry{agenda, agenda, event, event}, []entry{video, video})
	if len(got) != 2 || got[0].SourceID != "board-of-directors/2026-10-23" || got[1].SourceID != "board-of-directors/2026-12-11" {
		t.Errorf("meetings = %+v, want one per clip and event", got)
	}
}

func TestRecordingNeedsAVideoItemWithAnEnclosure(t *testing.T) {
	noMedia := videoItem(1438, "2026-11-13 10:00")
	noMedia.enclosure = ""
	got := meetingsFrom(t, []entry{clipItem(1438, "2026-11-13 10:00")}, []entry{noMedia})
	assertMeetings(t, got, []sources.Meeting{{
		SourceID:  "board-of-directors/2026-11-13",
		Title:     "Board of Directors",
		StartsAt:  time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC),
		Status:    sources.StatusScheduled,
		PageURL:   clipAgenda(1438),
		AgendaURL: clipAgenda(1438),
	}})
}

func TestVideoWithoutAnAgendaIsHeldAndHasNoAgendaURL(t *testing.T) {
	got := meetingsFrom(t, nil, []entry{videoItem(1437, "2026-11-13 10:00")})
	assertMeetings(t, got, []sources.Meeting{{
		SourceID:     "board-of-directors/2026-11-13",
		Title:        "Board of Directors",
		StartsAt:     time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC),
		Status:       sources.StatusHeld,
		PageURL:      mediaPlayer(1437),
		RecordingURL: mediaPlayer(1437),
	}})
}

func TestVideoItemWithoutMediaOrAgendaStillListsTheMeeting(t *testing.T) {
	noMedia := videoItem(1437, "2026-11-13 10:00")
	noMedia.enclosure = ""
	got := meetingsFrom(t, nil, []entry{noMedia})
	if len(got) != 1 || got[0].PageURL != mediaPlayer(1437) || got[0].RecordingURL != "" || got[0].Status != sources.StatusScheduled {
		t.Errorf("meetings = %+v, want a scheduled meeting whose page is the player", got)
	}
}

func TestFutureClipWithAVideoIsNotHeldYet(t *testing.T) {
	got := meetingsFrom(t, nil, []entry{videoItem(1440, "2026-12-11 10:00")})
	if len(got) != 1 || got[0].Status != sources.StatusScheduled {
		t.Errorf("meetings = %+v, want one scheduled meeting", got)
	}
}

func TestEventsInTheVideoFeedAreIgnored(t *testing.T) {
	event := eventItem(612, "2026-12-11", "2026-12-04 09:00")
	got := meetingsFrom(t, nil, []entry{event})
	if len(got) != 0 {
		t.Errorf("meetings = %+v, want none", got)
	}
}

func TestClipStartTimesAreLocalWallClockConvertedToUTC(t *testing.T) {
	tests := []struct {
		local string
		want  time.Time
	}{
		{"2026-11-13 10:00", time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)},
		{"2026-10-23 10:15", time.Date(2026, 10, 23, 17, 15, 0, 0, time.UTC)},
		{"2026-11-01 01:30", time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.local, func(t *testing.T) {
			got := meetingsFrom(t, []entry{clipItem(1, tc.local)}, nil)
			if len(got) != 1 || !got[0].StartsAt.Equal(tc.want) || got[0].StartsAt.Location() != time.UTC {
				t.Errorf("meetings = %+v, want start %s", got, tc.want)
			}
		})
	}
}

func TestClipWithUnreadableDatePartsUsesTheTitleDateAndDefaultTime(t *testing.T) {
	for _, p := range []string{"", "yr='2026' mo='11' day='13' hr='ten' min='00'", "yr='2026' mo='13' day='13' hr='10' min='00'", "yr='2026' mo='02' day='30' hr='10' min='00'", "yr='2026' mo='11' day='13' hr='24' min='00'"} {
		t.Run(p, func(t *testing.T) {
			item := clipItem(1438, "2026-11-13 14:00")
			item.parts = p
			got := meetingsFrom(t, []entry{item}, nil)
			if len(got) != 1 || !got[0].StartsAt.Equal(time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)) {
				t.Errorf("meetings = %+v, want 10:00 local on the title's date", got)
			}
		})
	}
}

func TestClipWithoutATitleDateUsesItsDateParts(t *testing.T) {
	got := meetingsFrom(t, []entry{clipItem(1438, "2026-11-13 10:00").titled("Board of Directors")}, nil)
	if len(got) != 1 || got[0].SourceID != "board-of-directors/2026-11-13" {
		t.Errorf("meetings = %+v, want the id dated from pubDateParts", got)
	}
}

func TestBoardNamesAndCancellations(t *testing.T) {
	tests := []struct {
		name      string
		board     bool
		title     string
		key       string
		cancelled bool
	}{
		{"Board of Directors", true, "Board of Directors", "board-of-directors", false},
		{"board  of directors", true, "Board of Directors", "board-of-directors", false},
		{"Special Board of Directors", true, "Special Board of Directors", "special-board-of-directors", false},
		{"Board of Directors - Special Meeting", true, "Board of Directors - Special Meeting", "board-of-directors-special-meeting", false},
		{"Board Meeting - Emergency", true, "Board Meeting - Emergency", "board-meeting-emergency", false},
		{"Board Meeting and Workshop", true, "Board Meeting and Workshop", "board-meeting-and-workshop", false},
		{"Special Board of Directors' Annual Strategic Session (8/26 + 8/27)", true, "Special Board of Directors' Annual Strategic Session", "special-board-of-directors-annual-strategic-session", false},
		{"Metrolink Board Meeting (9/9/11)", true, "Metrolink Board Meeting", "metrolink-board-meeting", false},
		{"Board of Directors - CORRECTED AGENDA", true, "Board of Directors", "board-of-directors", false},
		{"Board of Directors - Revised", true, "Board of Directors", "board-of-directors", false},
		{"*Board of Directors", true, "Board of Directors", "board-of-directors", false},
		{"Board of Directors - CANCELLED", true, "Board of Directors", "board-of-directors", true},
		{"Board Meeting - Cancelled", true, "Board Meeting", "board-meeting", true},
		{"Board of Directors (Cancelled)", true, "Board of Directors", "board-of-directors", true},
		{"CANCELLED - Board of Directors", true, "Board of Directors", "board-of-directors", true},
		{"Cancelled: Board of Directors", true, "Board of Directors", "board-of-directors", true},
		{"Executive Committee", false, "", "", false},
		{"Audit and Finance Committee - Cancelled", false, "", "", false},
		{"Board Workshop", false, "", "", false},
		{"Board Meeting - Item No. 8 - Attachment A", false, "", "", false},
		{"Special Board Meeting - Item 12 - Recovery Plan Framework", false, "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := eventItem(612, "2026-12-04", "2026-11-27 09:00").titled(tc.name + " - Dec 04, 2026")
			got := meetingsFrom(t, []entry{item}, nil)
			if !tc.board {
				if len(got) != 0 {
					t.Errorf("meetings = %+v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("meetings = %+v, want one", got)
			}
			m := got[0]
			if m.Title != tc.title || m.SourceID != tc.key+"/2026-12-04" || (m.Status == sources.StatusCancelled) != tc.cancelled {
				t.Errorf("meeting = %+v, want title %q, id %s/2026-12-04, cancelled %v", m, tc.title, tc.key, tc.cancelled)
			}
		})
	}
}

func TestAMeetingCancelledAfterItsAgendaKeepsItsSourceID(t *testing.T) {
	scheduled := meetingsFrom(t, []entry{eventItem(612, "2026-12-04", "2026-11-27 09:00")}, nil)
	cancelled := meetingsFrom(t, []entry{eventItem(612, "2026-12-04", "2026-11-27 09:00").titled("CANCELLED - Board of Directors - Dec 04, 2026")}, nil)
	if len(scheduled) != 1 || len(cancelled) != 1 || scheduled[0].SourceID != cancelled[0].SourceID || cancelled[0].Status != sources.StatusCancelled {
		t.Errorf("scheduled = %+v\ncancelled = %+v\nwant one meeting that turns cancelled", scheduled, cancelled)
	}
}

func TestCancelledClipHasNoRecording(t *testing.T) {
	item := clipItem(1438, "2026-11-13 10:00").titled("Board of Directors - CANCELLED - Nov 13, 2026")
	video := videoItem(1438, "2026-11-13 10:00").titled("Board of Directors - CANCELLED - Nov 13, 2026")
	got := meetingsFrom(t, []entry{item}, []entry{video})
	if len(got) != 1 || got[0].Status != sources.StatusCancelled || got[0].RecordingURL != "" || got[0].PageURL != clipAgenda(1438) {
		t.Errorf("meetings = %+v, want one cancelled meeting without a recording", got)
	}
}

func TestDroppedTitlesMentioningTheBoardAreLogged(t *testing.T) {
	var logs bytes.Buffer
	a, err := granicus.New(granicus.Metrolink, sources.Deps{
		Fetcher: serving(rss(
			eventItem(612, "2026-12-04", "2026-11-27 09:00").titled("Board Workshop - Dec 04, 2026"),
			eventItem(613, "2026-12-04", "2026-11-27 09:00").titled("Executive Committee - Dec 04, 2026"),
		), rss()),
		Clock:  sourcestest.NewClock(recordedAt),
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Meetings(t.Context(), checkWindow(recordedAt)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "Board Workshop") || strings.Contains(logs.String(), "Executive Committee") {
		t.Errorf("logs = %s, want only the dropped board title", logs.String())
	}
}

func TestMeetingsAreOrderedByStart(t *testing.T) {
	got := meetingsFrom(t, []entry{
		eventItem(700, "2026-12-11", "2026-12-04 09:00"),
		clipItem(1438, "2026-11-13 10:00"),
		eventItem(701, "2026-12-04", "2026-11-27 09:00"),
	}, nil)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.SourceID)
	}
	want := "board-of-directors/2026-11-13,board-of-directors/2026-12-04,board-of-directors/2026-12-11"
	if strings.Join(ids, ",") != want {
		t.Errorf("order = %v, want %s", ids, want)
	}
}
