package vctc_test

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/vctc"
)

var recordedAt = time.Date(2026, 11, 18, 20, 45, 0, 0, time.UTC)

const (
	postsURL = "https://www.goventura.org/wp-json/wp/v2/meeting?_fields=id%2Clink%2Ctitle%2Ccontent%2Cmeta%2Cmeeting_type&meeting_type=25%2C33&order=desc&orderby=date&per_page=20"
	feedURL  = "https://www.youtube.com/feeds/videos.xml?channel_id=UCer8W37pepfQH8mhDbTBRjg"
	uploads  = "https://www.goventura.org/wp-content/uploads/"
)

var losAngeles = func() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		panic(err)
	}
	return loc
}()

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := fs.ReadFile(os.DirFS("testdata"), name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func serving(posts, feed []byte) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if strings.HasPrefix(req.URL, "https://www.youtube.com/") {
			return fetch.Response{Status: 200, ContentType: "text/xml; charset=UTF-8", Body: feed, FinalURL: req.URL}, nil
		}
		return fetch.Response{Status: 200, ContentType: "application/json; charset=UTF-8", Body: posts, FinalURL: req.URL}, nil
	}}
}

func withYouTube() vctc.Config {
	cfg := vctc.VCTC
	cfg.YouTube = vctc.VCTCYouTube
	return cfg
}

func adapter(t *testing.T, cfg vctc.Config, fetcher fetch.Fetcher, now time.Time) *vctc.Adapter {
	t.Helper()
	a, err := vctc.New(cfg, sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(now)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func logged(t *testing.T, cfg vctc.Config, fetcher fetch.Fetcher, now time.Time) (*vctc.Adapter, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a, err := vctc.New(cfg, sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(now), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	return a, &buf
}

func checkWindow(now time.Time) sources.Window {
	return sources.Window{From: now.AddDate(0, 0, -30), To: now.AddDate(0, 0, 90)}
}

var sinceApril = sources.Window{From: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}

func meeting(id, title string, startsAt time.Time, slugPath, agenda string) sources.Meeting {
	return sources.Meeting{
		SourceID:  id,
		Title:     title,
		StartsAt:  startsAt,
		Status:    sources.StatusScheduled,
		PageURL:   "https://www.goventura.org/meeting/" + slugPath + "/",
		AgendaURL: uploads + agenda,
	}
}

func held(m sources.Meeting, videoID string) sources.Meeting {
	m.Status = sources.StatusHeld
	m.RecordingURL = "https://www.youtube.com/watch?v=" + videoID
	return m
}

func presented(m sources.Meeting) sources.Meeting {
	m.Status = sources.StatusHeld
	return m
}

func recordedMeetings() []sources.Meeting {
	return []sources.Meeting{
		meeting("1116", "Commission Meeting – April 2026", time.Date(2026, 4, 3, 16, 0, 0, 0, time.UTC), "commission-meeting-april-2026", "2026/03/VCTC-APRIL-3-2026-AGENDA.pdf"),
		meeting("23052", "Commission Meeting – May 2026", time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC), "commission-meeting-may-2026", "2026/04/VCTC-MAY-1-2026-AGENDA-1.pdf"),
		meeting("23053", "Commission Meeting – June 2026", time.Date(2026, 6, 5, 16, 0, 0, 0, time.UTC), "commission-meeting-june-2026", "2026/05/VCTC-JUNE-5-2026-AGENDA.pdf"),
		meeting("22913", "Commission Meeting – July 2026", time.Date(2026, 7, 10, 16, 0, 0, 0, time.UTC), "commission-meeting-july-2026", "2026/07/VCTC-July-10-2026-Agenda-1.pdf"),
		presented(meeting("23860", "Commission Meeting – September 2026", time.Date(2026, 9, 11, 16, 0, 0, 0, time.UTC), "commission-meeting-september-2026", "2026/09/AGENDA-COMMISSION-9.11.26.pdf")),
		presented(meeting("24063", "Commission Meeting – October 2026", time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC), "commission-meeting-october-2026", "2026/10/AMENDED-AGENDA-COMMISSION-10.9.26.pdf")),
		presented(meeting("24950", "Commission Meeting – November 2026", time.Date(2026, 11, 6, 17, 0, 0, 0, time.UTC), "commission-meeting-november-2026", "2026/10/AGENDA-COMMISSION-11.6.26.pdf")),
	}
}

func assertMeetings(t *testing.T, got, want []sources.Meeting) {
	t.Helper()
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d meetings, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("meeting %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestCommissionMeetingsFromRecordedResponse(t *testing.T) {
	fake := serving(fixture(t, "vctc_commission_meetings.json"), nil)
	a, logs := logged(t, vctc.VCTC, fake, recordedAt)

	got, err := a.Meetings(t.Context(), sinceApril)
	if err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, recordedMeetings())
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].URL != postsURL {
		t.Errorf("requests = %+v, want one GET of %s", calls, postsURL)
	}
	for _, post := range []string{"post=23053", "post=23052"} {
		if !strings.Contains(logs.String(), "level=WARN msg=\"vctc: board meeting post has no meeting date; using the date in its agenda link at the configured start time\" host=www.goventura.org "+post) {
			t.Errorf("no warning for %s, the posts without a meeting date in the window:\n%s", post, logs)
		}
	}
}

func TestDailyCheckWindowOnTheRecordingDay(t *testing.T) {
	got, err := adapter(t, vctc.VCTC, serving(fixture(t, "vctc_commission_meetings.json"), nil), recordedAt).Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, recordedMeetings()[6:])
}

func TestRecordingsFromRecordedYouTubeFeed(t *testing.T) {
	fake := serving(fixture(t, "vctc_commission_meetings.json"), fixture(t, "vctc_youtube_feed.xml"))
	a, logs := logged(t, withYouTube(), fake, recordedAt)

	got, err := a.Meetings(t.Context(), sinceApril)
	if err != nil {
		t.Fatal(err)
	}
	want := recordedMeetings()
	want[3] = held(want[3], "D9kOyNeb8Tc")
	want[4] = held(want[4], "PzRWxVqFYbA")
	want[5] = held(want[5], "5PUS3Zwz4Ek")
	want[6] = held(want[6], "3WbJq9XWnNs")
	assertMeetings(t, got, want)
	if calls := fake.Calls(); len(calls) != 2 || calls[1].URL != feedURL {
		t.Errorf("requests = %+v, want the posts and then %s", calls, feedURL)
	}
	if !strings.Contains(logs.String(), `msg="vctc: video title date differs from the meeting date; matched by publish time" host=www.goventura.org source_id=24950 video=3WbJq9XWnNs`) {
		t.Errorf("no log for the November video titled November 11:\n%s", logs)
	}
}

func TestUpcomingPostFromRecordedResponse(t *testing.T) {
	cfg := vctc.VCTC
	cfg.Boards = []vctc.Board{{TypeID: 28, Name: "TTAC"}}
	cfg.CommitteeWords = nil
	capturedAt := time.Date(2026, 11, 18, 20, 43, 0, 0, time.UTC)

	got, err := adapter(t, cfg, serving(fixture(t, "vctc_mixed_types_upcoming.json"), nil), capturedAt).Meetings(t.Context(), checkWindow(capturedAt))
	if err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, []sources.Meeting{
		meeting("25015", "TTAC – November 2026", time.Date(2026, 11, 18, 21, 30, 0, 0, time.UTC), "ttac-november-2026", "2026/11/AGENDA-TTAC-11.18.26.pdf"),
	})
}

func TestCommitteesInTheResponseAreDropped(t *testing.T) {
	got, err := adapter(t, withYouTube(), serving(fixture(t, "vctc_mixed_types_upcoming.json"), fixture(t, "vctc_youtube_feed.xml")), recordedAt).Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, []sources.Meeting{held(recordedMeetings()[6], "3WbJq9XWnNs")})
}

func TestRecordedSpecialMeetingDatedFromItsAgendaLink(t *testing.T) {
	body := fixture(t, "vctc_special_meetings.json")
	got, err := adapter(t, vctc.VCTC, serving(body, nil), recordedAt).Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil || len(got) != 0 {
		t.Errorf("Meetings in 2026 = %+v, %v; want none and no error", got, err)
	}

	nov2023 := sources.Window{From: time.Date(2023, 10, 15, 0, 0, 0, 0, time.UTC), To: time.Date(2023, 12, 15, 0, 0, 0, 0, time.UTC)}
	got, err = adapter(t, vctc.VCTC, serving(body, nil), recordedAt).Meetings(t.Context(), nov2023)
	if err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, []sources.Meeting{
		meeting("1454", "Special Meeting – November 2023", time.Date(2023, 11, 3, 16, 0, 0, 0, time.UTC), "special-meeting-november-2023", "2023/11/Special-Meeting-Agenda-for-11.3.2023_For-Posting-GCO.pdf"),
	})
}

func TestConstructorRegistersTheAdapter(t *testing.T) {
	registry := sources.NewRegistry(sources.Deps{Fetcher: serving(fixture(t, "vctc_commission_meetings.json"), nil), Clock: sourcestest.NewClock(recordedAt)},
		map[string]sources.Constructor{"vctc": vctc.Constructor(vctc.VCTC)})
	a, ok := registry.Lookup("vctc")
	if !ok {
		t.Fatal("vctc not registered")
	}
	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil || len(got) != 1 {
		t.Errorf("Meetings = %+v, %v", got, err)
	}
}
