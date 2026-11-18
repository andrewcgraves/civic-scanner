package legistar_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/legistar"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

type event map[string]any

func boardEvent(id int, date string, clock any) event {
	return event{
		"EventId":                id,
		"EventBodyId":            138,
		"EventBodyName":          "Board of Directors - Regular Board Meeting",
		"EventDate":              date + "T00:00:00",
		"EventTime":              clock,
		"EventAgendaStatusName":  "Final",
		"EventMinutesStatusName": "Draft",
		"EventAgendaFile":        nil,
		"EventMinutesFile":       nil,
		"EventMedia":             nil,
		"EventVideoPath":         nil,
		"EventInSiteURL":         metroPage(strconv.Itoa(id)),
	}
}

func (e event) with(key string, value any) event {
	out := event{}
	for k, v := range e {
		out[k] = v
	}
	out[key] = value
	return out
}

func encode(t *testing.T, events ...event) []byte {
	t.Helper()
	if events == nil {
		events = []event{}
	}
	body, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func onlyMeeting(t *testing.T, e event) sources.Meeting {
	t.Helper()
	got, err := laMetro(t, serving(encode(t, e))).Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d meetings, want 1: %+v", len(got), got)
	}
	return got[0]
}

func TestStartTimesAreLocalWallClockConvertedToUTC(t *testing.T) {
	tests := []struct {
		name  string
		date  string
		clock any
		want  time.Time
	}{
		{"winter, pacific standard time", "2026-01-22", "10:00 AM", time.Date(2026, 1, 22, 18, 0, 0, 0, time.UTC)},
		{"summer, pacific daylight time", "2026-07-23", "10:00 AM", time.Date(2026, 7, 23, 17, 0, 0, 0, time.UTC)},
		{"afternoon", "2026-09-16", "3:00 PM", time.Date(2026, 9, 16, 22, 0, 0, 0, time.UTC)},
		{"day daylight time starts", "2026-03-08", "10:00 AM", time.Date(2026, 3, 8, 17, 0, 0, 0, time.UTC)},
		{"day before daylight time starts", "2026-03-07", "10:00 AM", time.Date(2026, 3, 7, 18, 0, 0, 0, time.UTC)},
		{"day daylight time ends", "2026-11-01", "10:00 AM", time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)},
		{"day before daylight time ends", "2026-10-31", "10:00 AM", time.Date(2026, 10, 31, 17, 0, 0, 0, time.UTC)},
		{"evening that is the next day in utc", "2026-12-02", "6:30 PM", time.Date(2026, 12, 3, 2, 30, 0, 0, time.UTC)},
		{"half past midnight", "2026-12-02", "12:30 AM", time.Date(2026, 12, 2, 8, 30, 0, 0, time.UTC)},
		{"lower case and no space", "2026-12-02", " 9:15am ", time.Date(2026, 12, 2, 17, 15, 0, 0, time.UTC)},
		{"missing time is local midnight", "2026-12-02", nil, time.Date(2026, 12, 2, 8, 0, 0, 0, time.UTC)},
		{"empty time is local midnight", "2026-07-02", "", time.Date(2026, 7, 2, 7, 0, 0, 0, time.UTC)},
		{"unparseable time is local midnight", "2026-12-02", "TBD", time.Date(2026, 12, 2, 8, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := onlyMeeting(t, boardEvent(1, tc.date, tc.clock)).StartsAt
			if !got.Equal(tc.want) || got.Location() != time.UTC {
				t.Errorf("StartsAt = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStatusFromAgendaStatusAndEvidenceOfTheMeeting(t *testing.T) {
	past := boardEvent(1, "2026-10-22", "10:00 AM")
	upcoming := boardEvent(2, "2026-12-03", "10:00 AM")
	tests := []struct {
		name string
		e    event
		want sources.Status
	}{
		{"past with a recording", past.with("EventMedia", "4309"), sources.StatusHeld},
		{"past with a video path", past.with("EventVideoPath", "https://video.example.org/1"), sources.StatusHeld},
		{"past with a minutes file", past.with("EventMinutesFile", "https://octa.legistar1.com/octa/meetings/2026/10/m.pdf"), sources.StatusHeld},
		{"past with final minutes", past.with("EventMinutesStatusName", "FINAL"), sources.StatusHeld},
		{"past with no evidence it took place", past, sources.StatusScheduled},
		{"past with draft minutes and no recording", past.with("EventMinutesStatusName", "Draft"), sources.StatusScheduled},
		{"upcoming", upcoming, sources.StatusScheduled},
		{"upcoming with a clip id already", upcoming.with("EventMedia", "4400"), sources.StatusScheduled},
		{"canceled, la metro spelling", past.with("EventAgendaStatusName", "Canceled"), sources.StatusCancelled},
		{"cancelled, octa spelling", upcoming.with("EventAgendaStatusName", "CANCELLED"), sources.StatusCancelled},
		{"cancelled wins over a recording", past.with("EventAgendaStatusName", "Canceled").with("EventMedia", "4309"), sources.StatusCancelled},
		{"later the same day it is still scheduled", boardEvent(3, "2026-11-17", "10:00 AM").with("EventMedia", "4400"), sources.StatusScheduled},
		{"earlier the same day with a recording", boardEvent(4, "2026-11-17", "4:00 AM").with("EventMedia", "4400"), sources.StatusHeld},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := onlyMeeting(t, tc.e).Status; got != tc.want {
				t.Errorf("Status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRecordingURL(t *testing.T) {
	e := boardEvent(1, "2026-10-22", "10:00 AM")
	tests := []struct {
		name string
		e    event
		want string
	}{
		{"no clip", e, ""},
		{"granicus clip", e.with("EventMedia", "4309"), "https://metro.granicus.com/MediaPlayer.php?view_id=2&clip_id=4309"},
		{"video path wins", e.with("EventMedia", "4309").with("EventVideoPath", "https://video.example.org/4309"), "https://video.example.org/4309"},
		{"blank clip", e.with("EventMedia", " "), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := onlyMeeting(t, tc.e).RecordingURL; got != tc.want {
				t.Errorf("RecordingURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNoRecordingURLWithoutAGranicusPlayer(t *testing.T) {
	cfg := laMetroConfig()
	cfg.Granicus = legistar.Granicus{}
	got := meetingsWith(t, cfg, serving(encode(t, boardEvent(1, "2026-10-22", "10:00 AM").with("EventMedia", "4309"))))
	if len(got) != 1 || got[0].RecordingURL != "" || got[0].Status != sources.StatusHeld {
		t.Errorf("meetings = %+v, want one held meeting without a recording url", got)
	}
}

func TestPagesUntilAShortPage(t *testing.T) {
	events := []event{
		boardEvent(1, "2026-10-01", "10:00 AM"),
		boardEvent(2, "2026-10-08", "10:00 AM"),
		boardEvent(3, "2026-10-15", "10:00 AM"),
		boardEvent(4, "2026-10-22", "10:00 AM"),
		boardEvent(5, "2026-10-29", "10:00 AM"),
	}
	fake := pagedFake(t, events, 2)
	cfg := laMetroConfig()
	cfg.PageSize = 2

	got := meetingsWith(t, cfg, fake)

	if len(got) != 5 {
		t.Fatalf("got %d meetings, want 5: %+v", len(got), got)
	}
	var pages []string
	for _, c := range fake.Calls() {
		u, err := url.Parse(c.URL)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, u.Query().Get("$skip")+"/"+u.Query().Get("$top"))
	}
	if want := []string{"0/2", "2/2", "4/2"}; !equalStrings(pages, want) {
		t.Errorf("pages requested = %v, want %v", pages, want)
	}
}

func TestAnExactlyFullLastPageCostsOneEmptyRequest(t *testing.T) {
	events := []event{boardEvent(1, "2026-10-01", "10:00 AM"), boardEvent(2, "2026-10-08", "10:00 AM")}
	fake := pagedFake(t, events, 2)
	cfg := laMetroConfig()
	cfg.PageSize = 2

	if got := meetingsWith(t, cfg, fake); len(got) != 2 {
		t.Fatalf("got %d meetings, want 2", len(got))
	}
	if n := len(fake.Calls()); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestAnEventRepeatedAcrossPagesIsReturnedOnce(t *testing.T) {
	first := encode(t, boardEvent(1, "2026-10-01", "10:00 AM"), boardEvent(2, "2026-10-08", "10:00 AM"))
	second := encode(t, boardEvent(2, "2026-10-08", "10:00 AM"))
	fake := bySkip(map[string][]byte{"0": first, "2": second})
	cfg := laMetroConfig()
	cfg.PageSize = 2

	got := meetingsWith(t, cfg, fake)
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d meetings, want 2", len(got))
	}
}

func laMetroConfig() legistar.Config {
	cfg := legistar.LAMetro
	cfg.BodyIDs = slices.Clone(cfg.BodyIDs)
	return cfg
}

func meetingsWith(t *testing.T, cfg legistar.Config, fetcher fetch.Fetcher) []sources.Meeting {
	t.Helper()
	a, err := legistar.New(cfg, sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(recordedAt)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func bySkip(pages map[string][]byte) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		u, err := url.Parse(req.URL)
		if err != nil {
			return fetch.Response{}, err
		}
		body, ok := pages[u.Query().Get("$skip")]
		if !ok {
			return fetch.Response{}, &fetch.Error{StatusCode: 404, Err: errors.New("no such page")}
		}
		return jsonResponse(req, body), nil
	}}
}

func pagedFake(t *testing.T, events []event, size int) *fetchtest.Fake {
	t.Helper()
	pages := map[string][]byte{}
	for skip := 0; skip <= len(events); skip += size {
		pages[strconv.Itoa(skip)] = encode(t, events[skip:min(skip+size, len(events))]...)
	}
	return bySkip(pages)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
