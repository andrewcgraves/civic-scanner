package granicus_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
)

type entry struct {
	title string
	link  string
	parts string
}

func boardItem(clip int, page func(int) string, local string) entry {
	return entry{title: "Board of Directors - Dec 11, 2026", link: page(clip), parts: datePartsAttrs(local)}
}

func datePartsAttrs(local string) string {
	t, err := time.Parse("2006-01-02 15:04", local)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("yr='%d' mo='%02d' day='%02d' hr='%02d' min='%02d' sec='0' tz='8'", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
}

func (e entry) with(title string) entry {
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
		fmt.Fprintf(&b, "<link>%s</link></item>", strings.ReplaceAll(e.link, "&", "&amp;"))
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

func TestUpcomingMeetingWithAnAgendaAndNoVideoIsScheduled(t *testing.T) {
	got := meetingsFrom(t, []entry{boardItem(1440, agendaViewer, "2026-12-11 10:00")}, nil)
	assertMeetings(t, got, []sources.Meeting{{
		SourceID:  "1440",
		Title:     "Board of Directors",
		StartsAt:  time.Date(2026, 12, 11, 18, 0, 0, 0, time.UTC),
		Status:    sources.StatusScheduled,
		PageURL:   agendaViewer(1440),
		AgendaURL: agendaViewer(1440),
	}})
}

func TestPastMeetingWithoutAVideoIsStillScheduled(t *testing.T) {
	got := meetingsFrom(t, []entry{boardItem(1437, agendaViewer, "2026-11-13 10:00")}, nil)
	if len(got) != 1 || got[0].Status != sources.StatusScheduled || got[0].RecordingURL != "" {
		t.Errorf("meetings = %+v, want one scheduled meeting without a recording", got)
	}
}

func TestVideoWithoutAnAgendaIsHeldAndHasNoAgendaURL(t *testing.T) {
	got := meetingsFrom(t, nil, []entry{boardItem(1437, mediaPlayer, "2026-11-13 10:00")})
	assertMeetings(t, got, []sources.Meeting{{
		SourceID:     "1437",
		Title:        "Board of Directors",
		StartsAt:     time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC),
		Status:       sources.StatusHeld,
		PageURL:      mediaPlayer(1437),
		RecordingURL: mediaPlayer(1437),
	}})
}

func TestFutureMeetingWithAVideoIsNotHeldYet(t *testing.T) {
	got := meetingsFrom(t, nil, []entry{boardItem(1440, mediaPlayer, "2026-12-11 10:00")})
	if len(got) != 1 || got[0].Status != sources.StatusScheduled {
		t.Errorf("meetings = %+v, want one scheduled meeting", got)
	}
}

func TestStartTimesAreLocalWallClockConvertedToUTC(t *testing.T) {
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
			got := meetingsFrom(t, []entry{boardItem(1, agendaViewer, tc.local)}, nil)
			if len(got) != 1 || !got[0].StartsAt.Equal(tc.want) || got[0].StartsAt.Location() != time.UTC {
				t.Errorf("meetings = %+v, want start %s", got, tc.want)
			}
		})
	}
}

func TestBoardNamesAndCancellations(t *testing.T) {
	tests := []struct {
		title     string
		board     bool
		name      string
		cancelled bool
	}{
		{"Board of Directors - Nov 13, 2026", true, "Board of Directors", false},
		{"Special Board of Directors - Dec 4, 2026", true, "Special Board of Directors", false},
		{"Special Board Meeting - Dec 4, 2026", true, "Special Board Meeting", false},
		{"board  of directors - Dec 4, 2026", true, "board of directors", false},
		{"Board of Directors - CANCELLED - Dec 4, 2026", true, "Board of Directors", true},
		{"Board Meeting - Cancelled - Dec 4, 2026", true, "Board Meeting", true},
		{"Board of Directors (Cancelled) - Dec 4, 2026", true, "Board of Directors", true},
		{"Board of Directors", true, "Board of Directors", false},
		{"Executive Committee - Dec 4, 2026", false, "", false},
		{"Audit and Finance Committee - Cancelled - Dec 4, 2026", false, "", false},
		{"Board Workshop - Dec 4, 2026", false, "", false},
		{"Special Board Meeting and Annual Workshop - Dec 4, 2026", false, "", false},
		{"Board of Directors - Item No. 8 - Attachment A - Dec 4, 2026", false, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.title, func(t *testing.T) {
			item := boardItem(1440, agendaViewer, "2026-12-04 10:00").with(tc.title)
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
			if got[0].Title != tc.name || (got[0].Status == sources.StatusCancelled) != tc.cancelled {
				t.Errorf("meeting = %+v, want title %q, cancelled %v", got[0], tc.name, tc.cancelled)
			}
		})
	}
}

func TestClipsInBothFeedsAreMergedAndRepeatsCountOnce(t *testing.T) {
	agenda := boardItem(1436, agendaViewer, "2026-10-23 10:15")
	video := boardItem(1436, mediaPlayer, "2026-10-23 10:15")
	got := meetingsFrom(t, []entry{agenda, agenda}, []entry{video, video})
	assertMeetings(t, got, []sources.Meeting{heldBoardMeeting(1436, time.Date(2026, 10, 23, 17, 15, 0, 0, time.UTC))})
}

func TestMeetingsAreOrderedByStartThenClip(t *testing.T) {
	got := meetingsFrom(t, []entry{
		boardItem(1450, agendaViewer, "2026-12-11 10:00"),
		boardItem(1449, agendaViewer, "2026-12-11 10:00"),
		boardItem(1441, agendaViewer, "2026-12-04 10:00"),
	}, nil)
	var ids []string
	for _, m := range got {
		ids = append(ids, m.SourceID)
	}
	if strings.Join(ids, ",") != "1441,1449,1450" {
		t.Errorf("order = %v, want 1441,1449,1450", ids)
	}
}
