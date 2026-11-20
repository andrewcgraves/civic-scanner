package primegov_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/primegov"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

var recordedAt = time.Date(2026, 11, 19, 7, 30, 0, 0, time.UTC)

const (
	upcomingURL = "https://sbcta.primegov.com/api/v2/PublicPortal/ListUpcomingMeetings"
	archivedURL = "https://sbcta.primegov.com/api/v2/PublicPortal/ListArchivedMeetings?year="
	packetURL   = "https://sbcta.primegov.com/Public/CompiledDocument?meetingTemplateId="
	pageURL     = "https://www.gosbcta.com/board/upcoming-meetings-agenda/"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := fs.ReadFile(os.DirFS("testdata"), name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func serving(upcoming []byte, archived map[int][]byte) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		body := upcoming
		if year, ok := strings.CutPrefix(req.URL, archivedURL); ok {
			body = []byte("[]")
			for y, b := range archived {
				if year == strconv.Itoa(y) {
					body = b
				}
			}
		}
		return fetch.Response{Status: 200, ContentType: "application/json; charset=utf-8", Body: body, FinalURL: req.URL}, nil
	}}
}

func recorded(t *testing.T) *fetchtest.Fake {
	return serving(fixture(t, "sbcta_upcoming.json"), map[int][]byte{2026: fixture(t, "sbcta_archived_2026.json")})
}

func adapter(t *testing.T, cfg primegov.Config, fetcher fetch.Fetcher, now time.Time) *primegov.Adapter {
	t.Helper()
	a, err := primegov.New(cfg, sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(now)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func logged(t *testing.T, cfg primegov.Config, fetcher fetch.Fetcher, now time.Time) (*primegov.Adapter, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a, err := primegov.New(cfg, sources.Deps{Fetcher: fetcher, Clock: sourcestest.NewClock(now), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	return a, &buf
}

func checkWindow(now time.Time) sources.Window {
	return sources.Window{From: now.AddDate(0, 0, -30), To: now.AddDate(0, 0, 90)}
}

var wide = sources.Window{From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)}

func board(id string, startsAt time.Time, status sources.Status, packet string) sources.Meeting {
	m := sources.Meeting{SourceID: id, Title: "Board of Directors", StartsAt: startsAt, Status: status, PageURL: pageURL}
	if packet != "" {
		m.AgendaURL = packetURL + packet + "&compileOutputType=1"
	}
	return m
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

func urls(calls []fetch.Request) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.URL
	}
	return out
}

func TestSBCTABoardMeetingsInTheDailyWindowFromRecordedResponses(t *testing.T) {
	fake := recorded(t)
	a, logs := logged(t, primegov.SBCTA, fake, recordedAt)

	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, []sources.Meeting{
		board("100", time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC), sources.StatusHeld, "645"),
		board("101", time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC), sources.StatusScheduled, ""),
		board("144", time.Date(2027, 1, 6, 18, 0, 0, 0, time.UTC), sources.StatusScheduled, ""),
		board("145", time.Date(2027, 2, 3, 18, 0, 0, 0, time.UTC), sources.StatusScheduled, ""),
	})
	if want := []string{upcomingURL, archivedURL + "2026"}; !reflect.DeepEqual(urls(fake.Calls()), want) {
		t.Errorf("requests = %v, want %v", urls(fake.Calls()), want)
	}
	for _, c := range fake.Calls() {
		if c.MaxBytes != primegov.MaxListBytes {
			t.Errorf("request %s MaxBytes = %d, want %d", c.URL, c.MaxBytes, primegov.MaxListBytes)
		}
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("warnings on the recorded responses:\n%s", logs)
	}
	for _, id := range []string{"meeting=71 ", "meeting=103 ", "meeting=179 "} {
		if !strings.Contains(logs.String(), `level=INFO msg="primegov: dropped a meeting that names the board but is not of the board's committee" host=sbcta.primegov.com `+id) {
			t.Errorf("no info log for study session %s:\n%s", id, logs)
		}
	}
}

func TestSBCTAEveryBoardMeetingAndNoCommitteeFromRecordedResponses(t *testing.T) {
	a, logs := logged(t, primegov.SBCTA, recorded(t), recordedAt)

	got, err := a.Meetings(t.Context(), wide)
	if err != nil {
		t.Fatal(err)
	}
	assertMeetings(t, got, []sources.Meeting{
		board("67", time.Date(2026, 9, 2, 17, 0, 0, 0, time.UTC), sources.StatusHeld, "381"),
		board("86", time.Date(2026, 10, 7, 17, 0, 0, 0, time.UTC), sources.StatusHeld, "533"),
		board("100", time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC), sources.StatusHeld, "645"),
		board("101", time.Date(2026, 12, 2, 18, 0, 0, 0, time.UTC), sources.StatusScheduled, ""),
		board("144", time.Date(2027, 1, 6, 18, 0, 0, 0, time.UTC), sources.StatusScheduled, ""),
		board("145", time.Date(2027, 2, 3, 18, 0, 0, 0, time.UTC), sources.StatusScheduled, ""),
		board("146", time.Date(2027, 3, 3, 18, 0, 0, 0, time.UTC), sources.StatusScheduled, ""),
		board("147", time.Date(2027, 4, 7, 17, 0, 0, 0, time.UTC), sources.StatusScheduled, ""),
	})
	if !strings.Contains(logs.String(), `level=DEBUG msg="primegov: left out a dark month without documents" host=sbcta.primegov.com meeting=151 title="Board of Directors (DARK)"`) {
		t.Errorf("the dark board meeting of 2027-08-04 was not left out at debug level:\n%s", logs)
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "meeting=151 ") && !strings.Contains(line, "level=DEBUG") {
			t.Errorf("dark month logged above debug: %s", line)
		}
	}
	if !strings.Contains(logs.String(), `level=WARN msg="primegov: dropped a meeting of the board's committee whose title is not the board's" host=sbcta.primegov.com meeting=118 title="TEST Board PLEASE IGNORE" committee=1 type=2`) {
		t.Errorf("no warning for the test meeting filed under the board:\n%s", logs)
	}
	if !strings.Contains(logs.String(), `meeting=175 title="Board of Directors Metro Valley Study Session" committee=6`) {
		t.Errorf("study session filed under the transit committee was not logged:\n%s", logs)
	}
}

func TestSBCTARecordedResponsesHoldCommitteesCancellationsAndRevisions(t *testing.T) {
	type raw struct {
		ID          int    `json:"id"`
		CommitteeID int    `json:"committeeId"`
		Title       string `json:"title"`
		Documents   []struct {
			Name string `json:"templateName"`
		} `json:"documentList"`
	}
	var all []raw
	for _, name := range []string{"sbcta_upcoming.json", "sbcta_archived_2026.json"} {
		var list []raw
		if err := json.Unmarshal(fixture(t, name), &list); err != nil {
			t.Fatal(err)
		}
		all = append(all, list...)
	}
	committees, cancelled, revised := 0, 0, 0
	for _, r := range all {
		if r.CommitteeID != 1 {
			committees++
		}
		if strings.Contains(r.Title, "CANCELLED") {
			cancelled++
		}
		for _, d := range r.Documents {
			if d.Name == "REVISED ITEMS" {
				revised++
			}
		}
	}
	if committees < 20 || cancelled < 3 || revised != 1 {
		t.Errorf("fixtures hold %d committee meetings, %d cancelled and %d revised; the tests rely on them", committees, cancelled, revised)
	}
}
