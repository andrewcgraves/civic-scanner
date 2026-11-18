package granicus_test

import (
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/granicus"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

var recordedAt = time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)

const (
	agendasFeed = "https://metrolink.granicus.com/ViewPublisherRSS.php?view_id=8&mode=agendas"
	vodFeed     = "https://metrolink.granicus.com/ViewPublisherRSS.php?view_id=8&mode=vod"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := fs.ReadFile(os.DirFS("testdata"), name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func serving(agendas, vod []byte) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		body := agendas
		if strings.HasSuffix(req.URL, "mode=vod") {
			body = vod
		}
		return fetch.Response{Status: 200, ContentType: "text/xml;charset=utf-8", Body: body, FinalURL: req.URL}, nil
	}}
}

func recorded(t *testing.T) *fetchtest.Fake {
	t.Helper()
	return serving(fixture(t, "metrolink_agendas.xml"), fixture(t, "metrolink_vod.xml"))
}

func metrolink(t *testing.T, fetcher fetch.Fetcher) *granicus.Adapter {
	t.Helper()
	a, err := granicus.New(granicus.Metrolink, sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(recordedAt)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func checkWindow(now time.Time) sources.Window {
	return sources.Window{From: now.AddDate(0, 0, -30), To: now.AddDate(0, 0, 90)}
}

func clipAgenda(clip int) string {
	return fmt.Sprintf("https://metrolink.granicus.com/AgendaViewer.php?view_id=8&clip_id=%d", clip)
}

func eventAgenda(event int) string {
	return fmt.Sprintf("https://metrolink.granicus.com/AgendaViewer.php?view_id=8&event_id=%d", event)
}

func mediaPlayer(clip int) string {
	return fmt.Sprintf("https://metrolink.granicus.com/MediaPlayer.php?view_id=8&clip_id=%d", clip)
}

func heldBoardMeeting(clip int, startsAt time.Time) sources.Meeting {
	local := startsAt.In(losAngeles)
	return sources.Meeting{
		SourceID:     "board-of-directors/" + local.Format(time.DateOnly),
		Title:        "Board of Directors",
		StartsAt:     startsAt,
		Status:       sources.StatusHeld,
		PageURL:      mediaPlayer(clip),
		AgendaURL:    clipAgenda(clip),
		RecordingURL: mediaPlayer(clip),
	}
}

var losAngeles = func() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		panic(err)
	}
	return loc
}()

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

func TestMetrolinkBoardMeetingsFromRecordedFeeds(t *testing.T) {
	fake := recorded(t)
	window := sources.Window{From: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}

	got, err := metrolink(t, fake).Meetings(t.Context(), window)
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, []sources.Meeting{
		heldBoardMeeting(1405, time.Date(2026, 7, 24, 17, 0, 0, 0, time.UTC)),
		heldBoardMeeting(1411, time.Date(2026, 9, 25, 17, 15, 0, 0, time.UTC)),
		heldBoardMeeting(1421, time.Date(2026, 10, 9, 21, 30, 0, 0, time.UTC)),
		heldBoardMeeting(1436, time.Date(2026, 10, 23, 17, 15, 0, 0, time.UTC)),
		heldBoardMeeting(1438, time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)),
	})

	calls := fake.Calls()
	if len(calls) != 2 || calls[0].URL != agendasFeed || calls[1].URL != vodFeed {
		t.Errorf("requests = %+v, want the agenda feed then the video feed", calls)
	}
}

func TestDailyCheckWindowKeepsOnlyMeetingsInside(t *testing.T) {
	got, err := metrolink(t, recorded(t)).Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, []sources.Meeting{
		heldBoardMeeting(1436, time.Date(2026, 10, 23, 17, 15, 0, 0, time.UTC)),
		heldBoardMeeting(1438, time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)),
	})
}

func TestCommitteeItemsInTheRecordedFeedsAreDropped(t *testing.T) {
	window := sources.Window{From: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}
	got, err := metrolink(t, recorded(t)).Meetings(t.Context(), window)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("meetings = %+v, want none: the only item that day is a committee", got)
	}
}

func TestUpcomingEventsFromARecordedGranicusFeed(t *testing.T) {
	cfg := granicus.Config{
		Host:       "sacramento.granicus.com",
		ViewID:     21,
		BoardNames: []string{"Active Transportation Commission", "Housing Code Advisory and Appeals Board"},
		TimeZone:   "America/Los_Angeles",
		StartTime:  "17:30",
	}
	a, err := granicus.New(cfg, sources.Deps{
		Fetcher: serving(fixture(t, "sacramento_agendas_upcoming.xml"), []byte(`<rss version="2.0"><channel></channel></rss>`)),
		Clock:   sourcestest.NewClock(recordedAt),
	})
	if err != nil {
		t.Fatal(err)
	}
	window := sources.Window{From: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}

	got, err := a.Meetings(t.Context(), window)
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	sac := func(param string, id int) string {
		return fmt.Sprintf("https://sacramento.granicus.com/AgendaViewer.php?view_id=21&%s=%d", param, id)
	}
	assertMeetings(t, got, []sources.Meeting{
		{
			SourceID:  "housing-code-advisory-and-appeals-board/2026-07-22",
			Title:     "Housing Code Advisory and Appeals Board",
			StartsAt:  time.Date(2026, 7, 23, 0, 30, 0, 0, time.UTC),
			Status:    sources.StatusCancelled,
			PageURL:   sac("clip_id", 6828),
			AgendaURL: sac("clip_id", 6828),
		},
		{
			SourceID:  "active-transportation-commission/2026-10-15",
			Title:     "Active Transportation Commission",
			StartsAt:  time.Date(2026, 10, 16, 0, 45, 0, 0, time.UTC),
			Status:    sources.StatusScheduled,
			PageURL:   sac("clip_id", 6915),
			AgendaURL: sac("clip_id", 6915),
		},
		{
			SourceID:  "active-transportation-commission/2026-11-19",
			Title:     "Active Transportation Commission",
			StartsAt:  time.Date(2026, 11, 20, 1, 30, 0, 0, time.UTC),
			Status:    sources.StatusScheduled,
			PageURL:   sac("event_id", 6450),
			AgendaURL: sac("event_id", 6450),
		},
	})
}

func TestConstructorBuildsAnAdapterFromDeps(t *testing.T) {
	fake := recorded(t)
	adapter := granicus.Constructor(granicus.Metrolink)(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(recordedAt)})

	got, err := adapter.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(fake.Calls()) != 2 {
		t.Errorf("meetings = %d, requests = %d, want 2 and 2", len(got), len(fake.Calls()))
	}
}
