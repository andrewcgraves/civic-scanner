package legistar_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/legistar"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

var recordedAt = time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)

func jsonResponse(req fetch.Request, body []byte) fetch.Response {
	return fetch.Response{Status: 200, ContentType: "application/json; charset=utf-8", Body: body, FinalURL: req.URL}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func serving(body []byte) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		return jsonResponse(req, body), nil
	}}
}

func laMetro(t *testing.T, fetcher fetch.Fetcher) *legistar.Adapter {
	t.Helper()
	a, err := legistar.New(legistar.LAMetro, sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(recordedAt)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func checkWindow(now time.Time) sources.Window {
	return sources.Window{From: now.AddDate(0, 0, -30), To: now.AddDate(0, 0, 90)}
}

func metroPage(id string) string {
	return "https://metro.legistar.com/MeetingDetail.aspx?LEGID=" + id + "&GID=557&G=A5FAA737-A54D-4A6C-B1E8-FF70F765FA94"
}

func metroClip(id string) string {
	return "https://metro.granicus.com/MediaPlayer.php?view_id=2&clip_id=" + id
}

func assertMeetings(t *testing.T, got, want []sources.Meeting) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d meetings, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("meeting %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestLAMetroBoardMeetingsFromRecordedResponse(t *testing.T) {
	a := laMetro(t, serving(fixture(t, "metro_board_events.json")))

	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}

	regular := "Board of Directors - Regular Board Meeting"
	special := "Board of Directors - Special Board Meeting"
	want := []sources.Meeting{
		{
			SourceID:     "3354",
			Title:        regular,
			StartsAt:     time.Date(2026, 1, 22, 18, 0, 0, 0, time.UTC),
			Status:       sources.StatusHeld,
			PageURL:      metroPage("3354"),
			AgendaURL:    "https://metro.legistar1.com/metro/meetings/2026/1/3354_A_Board_of_Directors_-_Regular_Board_Meeting_26-01-22_Agenda.pdf",
			RecordingURL: metroClip("3888"),
		},
		{
			SourceID: "3253",
			Title:    regular,
			StartsAt: time.Date(2026, 6, 25, 17, 0, 0, 0, time.UTC),
			Status:   sources.StatusCancelled,
			PageURL:  metroPage("3253"),
		},
		{
			SourceID:     "3518",
			Title:        regular,
			StartsAt:     time.Date(2026, 7, 23, 17, 0, 0, 0, time.UTC),
			Status:       sources.StatusHeld,
			PageURL:      metroPage("3518"),
			AgendaURL:    "https://metro.legistar1.com/metro/meetings/2026/7/3518_A_Board_of_Directors_-_Regular_Board_Meeting_26-07-23_Agenda.pdf",
			RecordingURL: metroClip("4215"),
		},
		{
			SourceID:     "3580",
			Title:        special,
			StartsAt:     time.Date(2026, 9, 16, 22, 0, 0, 0, time.UTC),
			Status:       sources.StatusHeld,
			PageURL:      metroPage("3580"),
			AgendaURL:    "https://metro.legistar1.com/metro/meetings/2026/9/3580_A_Board_of_Directors_-_Special_Board_Meeting_26-09-16_Agenda.pdf",
			RecordingURL: metroClip("4255"),
		},
		{
			SourceID:     "3549",
			Title:        regular,
			StartsAt:     time.Date(2026, 10, 22, 17, 0, 0, 0, time.UTC),
			Status:       sources.StatusHeld,
			PageURL:      metroPage("3549"),
			AgendaURL:    "https://metro.legistar1.com/metro/meetings/2026/10/3549_A_Board_of_Directors_-_Regular_Board_Meeting_26-10-22_Agenda.pdf",
			RecordingURL: metroClip("4309"),
		},
		{
			SourceID: "3666",
			Title:    special,
			StartsAt: time.Date(2027, 1, 20, 17, 0, 0, 0, time.UTC),
			Status:   sources.StatusCancelled,
			PageURL:  metroPage("3666"),
		},
	}
	assertMeetings(t, got, want)
}

func TestQueryAsksForBoardEventsOnTheWindowsLocalDates(t *testing.T) {
	recorded := "https://webapi.legistar.com/v1/metro/events?%24filter=EventDate+ge+datetime%272026-01-01%27+and+EventDate+le+datetime%272027-01-31%27+and+%28EventBodyId+eq+138+or+EventBodyId+eq+199%29&%24orderby=EventDate%2CEventId&%24skip=0&%24top=1000"
	tests := []struct {
		name   string
		window sources.Window
	}{
		{
			name: "local midnight to the last minute of the day",
			window: sources.Window{
				From: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
				To:   time.Date(2027, 2, 1, 7, 59, 0, 0, time.UTC),
			},
		},
		{
			name: "utc dates that are the previous day in los angeles",
			window: sources.Window{
				From: time.Date(2026, 1, 2, 7, 59, 0, 0, time.UTC),
				To:   time.Date(2027, 2, 1, 7, 0, 0, 0, time.UTC),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := serving([]byte("[]"))
			if _, err := laMetro(t, fake).Meetings(t.Context(), tc.window); err != nil {
				t.Fatal(err)
			}
			calls := fake.Calls()
			if len(calls) != 1 || calls[0].URL != recorded {
				t.Errorf("requests = %+v, want one GET %s", calls, recorded)
			}
		})
	}
}

func TestCommitteeEventsAreDroppedEvenIfTheQueryReturnsThem(t *testing.T) {
	a := laMetro(t, serving(fixture(t, "metro_events_with_committee.json")))

	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "3549" {
		t.Errorf("meetings = %+v, want only the board meeting 3549", got)
	}
}
