package vctc_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/vctc"
)

type built struct {
	ID      int    `json:"id"`
	Link    string `json:"link"`
	Title   any    `json:"title"`
	Content any    `json:"content"`
	Meta    any    `json:"meta"`
	Types   []int  `json:"meeting_type"`
}

func commission(id int, month, date, clock, agenda string) built {
	return built{
		ID:      id,
		Link:    fmt.Sprintf("https://www.goventura.org/meeting/commission-meeting-%s/", strings.ToLower(strings.ReplaceAll(month, " ", "-"))),
		Title:   map[string]string{"rendered": "Commission Meeting &#8211; " + month},
		Content: map[string]any{"rendered": agendaSection(agenda), "protected": false},
		Meta:    map[string]any{"_acf_changed": false, "footnotes": "", "_meeting_date": date, "_meeting_time": clock},
		Types:   []int{25},
	}
}

func agendaSection(agendas ...string) string {
	var b strings.Builder
	b.WriteString(`<div class="wp-block-query"><h3 class="wp-block-post-title"><a href="https://www.goventura.org/meeting/ttac-november-2026/">TTAC – November 2026</a></h3></div>`)
	b.WriteString(`<h3 id="current-agenda" class="wp-block-heading">Current agenda</h3>`)
	for _, a := range agendas {
		if a == "" {
			continue
		}
		fmt.Fprintf(&b, `<div class="wp-block-file"><a href="%s">Agenda</a></div>`, a)
	}
	b.WriteString(`<h3 class="wp-block-heading">SUPPORTING DOCUMENTS</h3><div class="wp-block-file"><a href="https://www.goventura.org/wp-content/uploads/2026/10/attachment.pdf">Attachment</a></div>`)
	return b.String()
}

func (b built) titled(rendered string) built {
	b.Title = map[string]string{"rendered": rendered}
	return b
}

func (b built) typed(types ...int) built {
	b.Types = types
	return b
}

func (b built) content(rendered string) built {
	b.Content = map[string]any{"rendered": rendered}
	return b
}

func posts(t *testing.T, items ...any) []byte {
	t.Helper()
	if items == nil {
		items = []any{}
	}
	body, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type videoEntry struct {
	id        string
	title     string
	published string
}

func feed(entries ...videoEntry) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom"><title>Ventura County Transportation Commission</title>`)
	for _, e := range entries {
		fmt.Fprintf(&b, `<entry><id>yt:video:%[1]s</id><yt:videoId>%[1]s</yt:videoId><title>%[2]s</title><published>%[3]s</published></entry>`, e.id, e.title, e.published)
	}
	b.WriteString(`</feed>`)
	return []byte(b.String())
}

func meetingsOf(t *testing.T, cfg vctc.Config, body, feedBody []byte, now time.Time) []sources.Meeting {
	t.Helper()
	got, err := adapter(t, cfg, serving(body, feedBody), now).Meetings(t.Context(), checkWindow(now))
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	return got
}

func byID(ms []sources.Meeting) map[string]sources.Meeting {
	out := map[string]sources.Meeting{}
	for _, m := range ms {
		out[m.SourceID] = m
	}
	return out
}

func TestStartTimeIsTheMeetingTimeInTheAgencyZone(t *testing.T) {
	now := time.Date(2027, 2, 1, 12, 0, 0, 0, time.UTC)
	body := posts(t,
		commission(1, "March 2027", "2027-03-12", "09:00", ""),
		commission(2, "March 2027", "2027-03-19", "09:00", ""),
		commission(3, "April 2027", "2027-04-02", "1:30 PM", ""),
		commission(4, "April 2027", "2027-04-09", "13:30", ""),
		commission(5, "February 2027", "2027-02-05", "08:30:00", ""),
	)
	got := byID(meetingsOf(t, vctc.VCTC, body, nil, now))
	want := map[string]time.Time{
		"1": time.Date(2027, 3, 12, 17, 0, 0, 0, time.UTC),
		"2": time.Date(2027, 3, 19, 16, 0, 0, 0, time.UTC),
		"3": time.Date(2027, 4, 2, 20, 30, 0, 0, time.UTC),
		"4": time.Date(2027, 4, 9, 20, 30, 0, 0, time.UTC),
		"5": time.Date(2027, 2, 5, 16, 30, 0, 0, time.UTC),
	}
	for id, w := range want {
		if !got[id].StartsAt.Equal(w) {
			t.Errorf("post %s starts at %s, want %s", id, got[id].StartsAt, w)
		}
	}
}

func TestMissingOrUnreadableTimeUsesTheConfiguredStart(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	body := posts(t,
		commission(1, "December 2026", "2026-12-04", "", ""),
		commission(2, "January 2027", "2027-01-08", "morning", ""),
	)
	a, logs := logged(t, vctc.VCTC, serving(body, nil), now)
	got, err := a.Meetings(t.Context(), checkWindow(now))
	if err != nil {
		t.Fatal(err)
	}
	m := byID(got)
	if !m["1"].StartsAt.Equal(time.Date(2026, 12, 4, 17, 0, 0, 0, time.UTC)) || !m["2"].StartsAt.Equal(time.Date(2027, 1, 8, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("starts = %s, %s; want 09:00 local", m["1"].StartsAt, m["2"].StartsAt)
	}
	for _, want := range []string{
		`level=INFO msg="vctc: board meeting post has no start time; using the configured start time" host=www.goventura.org post=1`,
		`level=WARN msg="vctc: board meeting post has an unreadable start time; using the configured start time" host=www.goventura.org post=2 time=morning`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing log %q in:\n%s", want, logs)
		}
	}
}

func TestTitleMonthThatDisagreesWithTheMeetingDateIsLogged(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	body := posts(t,
		commission(1, "November 2026", "2026-12-04", "09:00", ""),
		commission(2, "December 2026", "2026-12-04", "09:00", "").titled("Commission Meeting – December 11, 2026"),
		commission(3, "January 2027", "2027-01-08", "09:00", ""),
	)
	a, logs := logged(t, vctc.VCTC, serving(body, nil), now)
	got, err := a.Meetings(t.Context(), checkWindow(now))
	if err != nil {
		t.Fatal(err)
	}
	m := byID(got)
	if !m["1"].StartsAt.Equal(time.Date(2026, 12, 4, 17, 0, 0, 0, time.UTC)) || !m["2"].StartsAt.Equal(time.Date(2026, 12, 4, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("starts = %s, %s; want the meeting date", m["1"].StartsAt, m["2"].StartsAt)
	}
	for _, post := range []string{"post=1", "post=2"} {
		if !strings.Contains(logs.String(), `msg="vctc: the post title names another date than the meeting date; using the meeting date" host=www.goventura.org `+post) {
			t.Errorf("no mismatch warning for %s:\n%s", post, logs)
		}
	}
	if strings.Contains(logs.String(), "using the meeting date\" host=www.goventura.org post=3") {
		t.Errorf("post 3 agrees but was logged:\n%s", logs)
	}
}

func TestCancellationMarkers(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	base := commission(0, "December 2026", "2026-12-04", "09:00", "")
	cases := []struct {
		title     string
		cancelled bool
	}{
		{"Commission Meeting &#8211; December 2026 (Cancelled)", true},
		{"Commission Meeting – December 2026 (Canceled)", true},
		{"Commission Meeting – December 2026 (CANCELLED) (2)", true},
		{"**CANCELLED** Commission Meeting – December 2026", true},
		{"Cancelled: Commission Meeting – December 2026", true},
		{"Commission Meeting – December 2026 - Cancelled", true},
		{"Commission Meeting – December 2026", false},
		{"Commission Meeting – December 2026 (Special Meeting)", false},
		{"Commission Meeting – December 2026 (Cancellation of the Measure Item Discussed)", false},
		{"Commission Meeting – December 2026 (Items Cancelled Elsewhere)", false},
		{"Commission Meeting – Cancelled Bus Routes Hearing", false},
	}
	for i, c := range cases {
		p := base
		p.ID = i + 1
		got := meetingsOf(t, vctc.VCTC, posts(t, p.titled(c.title)), nil, now)
		if len(got) != 1 {
			t.Errorf("%q: got %+v, want one meeting", c.title, got)
			continue
		}
		if (got[0].Status == sources.StatusCancelled) != c.cancelled {
			t.Errorf("%q: status %s, cancelled want %v", c.title, got[0].Status, c.cancelled)
		}
	}
}

func TestOnlyFullCommissionMeetingsAreKept(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	regular := commission(1, "December 2026", "2026-12-04", "09:00", "")
	body := posts(t,
		regular,
		commission(2, "December 2026", "2026-12-11", "10:00", "").titled("Special Meeting – December 2026").typed(33),
		commission(3, "December 2026", "2026-12-02", "13:30", "").titled("TRANSCOM – December 2026").typed(27),
		commission(4, "December 2026", "2026-12-14", "13:30", "").titled("Admin/Finance – December 2026 (Special Meeting)").typed(30, 33),
		commission(5, "December 2026", "2026-12-15", "09:00", "").titled("Special Meeting VCTC Goal Setting Session").typed(33),
		commission(6, "December 2026", "2026-12-16", "09:00", "").titled("Commission Workshop – December 2026"),
		commission(7, "December 2026", "2026-12-17", "09:00", "").titled("Special Meeting – December 2026").typed(25),
		commission(8, "December 2026", "2026-12-18", "09:00", "").typed(),
		commission(9, "December 2026", "2026-12-19", "17:00", "").titled("SPBL Community Meeting – December 2026").typed(33),
	)
	a, logs := logged(t, vctc.VCTC, serving(body, nil), now)
	got, err := a.Meetings(t.Context(), checkWindow(now))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, m := range got {
		ids = append(ids, m.SourceID)
	}
	if !slices.Equal(ids, []string{"1", "2"}) {
		t.Errorf("kept %v, want the regular and the special commission meeting", ids)
	}
	for _, want := range []string{
		`level=INFO msg="vctc: dropped a post that is not a configured board meeting" host=www.goventura.org post=4`,
		`level=WARN msg="vctc: dropped a post of a board meeting type whose title is not a configured board meeting" host=www.goventura.org post=5`,
		`level=WARN msg="vctc: dropped a post of a board meeting type whose title is not a configured board meeting" host=www.goventura.org post=6`,
		`level=WARN msg="vctc: dropped a post of a board meeting type whose title is not a configured board meeting" host=www.goventura.org post=7`,
		`level=INFO msg="vctc: dropped a post that is not a configured board meeting" host=www.goventura.org post=8`,
		`level=INFO msg="vctc: dropped a post that is not a configured board meeting" host=www.goventura.org post=9`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing log %q in:\n%s", want, logs)
		}
	}
	if strings.Contains(logs.String(), "post=3 ") {
		t.Errorf("a committee post was logged:\n%s", logs)
	}
}

func TestAgendaLinkUnderTheCurrentAgendaHeading(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	p := commission(1, "December 2026", "2026-12-04", "09:00", "")
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"none yet", agendaSection(), ""},
		{"one", agendaSection(uploads + "2026/11/AGENDA-COMMISSION-12.4.26.pdf"), uploads + "2026/11/AGENDA-COMMISSION-12.4.26.pdf"},
		{"relative", agendaSection("/wp-content/uploads/2026/11/AGENDA.pdf"), uploads + "2026/11/AGENDA.pdf"},
		{"amended beside the original", agendaSection(uploads+"2026/11/AGENDA.pdf", uploads+"2026/11/AMENDED-AGENDA.pdf"), uploads + "2026/11/AMENDED-AGENDA.pdf"},
		{"two plain", agendaSection(uploads+"2026/11/A.pdf", uploads+"2026/11/B.pdf"), uploads + "2026/11/A.pdf"},
		{"not http", agendaSection("mailto:clerk@goventura.org"), ""},
		{"unparseable", agendaSection("http://[::1"), ""},
		{"no heading", `<div class="wp-block-file"><a href="` + uploads + `x.pdf">Agenda</a></div>`, ""},
		{"heading last", `<h3>Current Agenda</h3><p>To be posted.</p>`, ""},
		{"supporting first", `<h3>SUPPORTING DOCUMENTS</h3><a href="` + uploads + `s.pdf">S</a><h3> Current  agenda </h3><a href="` + uploads + `a.pdf">Agenda</a>`, uploads + "a.pdf"},
	}
	for _, c := range cases {
		got := meetingsOf(t, vctc.VCTC, posts(t, p.content(c.content)), nil, now)
		if len(got) != 1 || got[0].AgendaURL != c.want {
			t.Errorf("%s: got %+v, want agenda %q", c.name, got, c.want)
		}
	}
}

func TestDuplicatePostsForOneDayAreBothKeptAndLogged(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	body := posts(t,
		commission(1, "December 2026", "2026-12-04", "09:00", ""),
		commission(2, "December 2026", "2026-12-04", "09:00", "").titled("Commission Meeting – December 2026 (2)"),
	)
	a, logs := logged(t, vctc.VCTC, serving(body, nil), now)
	got, err := a.Meetings(t.Context(), checkWindow(now))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.Contains(logs.String(), `msg="vctc: two posts for one board on one day; both are stored"`) {
		t.Errorf("got %+v, logs:\n%s", got, logs)
	}
}

func TestSourceIDsAreStableAcrossDailyRuns(t *testing.T) {
	var recorded []map[string]any
	if err := json.Unmarshal(fixture(t, "vctc_commission_meetings.json"), &recorded); err != nil {
		t.Fatal(err)
	}
	november := recorded[0]
	october := recorded[1]
	novContent := november["content"].(map[string]any)["rendered"].(string)
	withContent := func(post map[string]any, content string) map[string]any {
		out := map[string]any{}
		for k, v := range post {
			out[k] = v
		}
		out["content"] = map[string]any{"rendered": content}
		return out
	}
	posted := withContent(november, novContent[:strings.Index(novContent, `<h3 id="presentations"`)])
	amended := withContent(november, strings.Replace(posted["content"].(map[string]any)["rendered"].(string), "AGENDA-COMMISSION-11.6.26.pdf\">\n\tAgenda", "AMENDED-AGENDA-COMMISSION-11.6.26.pdf\">\n\tAmended Agenda", 1))
	december := commission(25100, "December 2026", "2026-12-04", "09:00", uploads+"2026/11/AGENDA-COMMISSION-12.4.26.pdf")
	januaryCancelled := commission(25200, "January 2027", "2027-01-08", "09:00", "").titled("Commission Meeting – January 2027 (Cancelled)")
	novVideo := videoEntry{"3WbJq9XWnNs", "VCTC Commission Meeting - November 11, 2026", "2026-11-07T06:42:53+00:00"}
	octVideo := videoEntry{"5PUS3Zwz4Ek", "VCTC Commission Meeting - October 9, 2026", "2026-10-10T05:52:17+00:00"}

	runs := []struct {
		now   time.Time
		posts []any
		feed  []byte
		want  map[string]sources.Status
	}{
		{time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC), []any{posted, october}, feed(octVideo), map[string]sources.Status{"24950": sources.StatusScheduled, "24063": sources.StatusHeld}},
		{time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC), []any{october, amended}, feed(octVideo), map[string]sources.Status{"24950": sources.StatusScheduled, "24063": sources.StatusHeld}},
		{time.Date(2026, 11, 6, 13, 0, 0, 0, time.UTC), []any{amended, october}, feed(octVideo), map[string]sources.Status{"24950": sources.StatusScheduled, "24063": sources.StatusHeld}},
		{time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC), []any{november, october}, feed(novVideo, octVideo), map[string]sources.Status{"24950": sources.StatusHeld, "24063": sources.StatusHeld}},
		{time.Date(2026, 11, 28, 13, 0, 0, 0, time.UTC), []any{december, november, october}, feed(novVideo, octVideo), map[string]sources.Status{"25100": sources.StatusScheduled, "24950": sources.StatusHeld}},
		{time.Date(2026, 12, 5, 13, 0, 0, 0, time.UTC), []any{october, januaryCancelled, november, december}, feed(novVideo), map[string]sources.Status{"25200": sources.StatusCancelled, "25100": sources.StatusScheduled, "24950": sources.StatusHeld}},
	}
	dates := map[string]string{}
	for i, run := range runs {
		got := byID(meetingsOf(t, withYouTube(), posts(t, run.posts...), run.feed, run.now))
		for id, status := range run.want {
			m, ok := got[id]
			if !ok {
				t.Errorf("run %d: no meeting %s in %v", i, id, got)
				continue
			}
			if m.Status != status {
				t.Errorf("run %d: meeting %s is %s, want %s", i, id, m.Status, status)
			}
			date := m.StartsAt.In(losAngeles).Format(time.DateOnly)
			if prev, seen := dates[id]; seen && prev != date {
				t.Errorf("run %d: source id %s moved from %s to %s", i, id, prev, date)
			}
			dates[id] = date
		}
		if i == 1 || i == 2 {
			if a := got["24950"].AgendaURL; a != uploads+"2026/10/AMENDED-AGENDA-COMMISSION-11.6.26.pdf" {
				t.Errorf("run %d: agenda %s, want the amended agenda", i, a)
			}
		}
	}
	want := map[string]string{"24063": "2026-10-09", "24950": "2026-11-06", "25100": "2026-12-04", "25200": "2027-01-08"}
	for id, date := range want {
		if dates[id] != date {
			t.Errorf("source id %s maps to %s, want %s", id, dates[id], date)
		}
	}
}

func TestRecordingMatchingRules(t *testing.T) {
	cfg := withYouTube()
	nov6 := commission(1, "November 2026", "2026-11-06", "09:00", "")
	special := commission(2, "November 2026", "2026-11-05", "09:00", "").titled("Special Meeting – November 2026").typed(33)
	video := func(id, published string) videoEntry {
		return videoEntry{id, "VCTC Commission Meeting - November 6, 2026", published}
	}
	after := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		posts []any
		feed  []byte
		now   time.Time
		want  map[string]string
	}{
		{"published 14 hours after the start", []any{nov6}, feed(video("aaaaaaaaaaa", "2026-11-07T06:42:53Z")), after, map[string]string{"1": "aaaaaaaaaaa"}},
		{"published before the start", []any{nov6}, feed(video("aaaaaaaaaaa", "2026-11-06T16:59:00Z")), after, map[string]string{"1": ""}},
		{"published after the delay", []any{nov6}, feed(video("aaaaaaaaaaa", "2026-11-09T17:00:00Z")), after, map[string]string{"1": ""}},
		{"two videos for one meeting", []any{nov6}, feed(video("aaaaaaaaaaa", "2026-11-07T06:00:00Z"), video("bbbbbbbbbbb", "2026-11-07T08:00:00Z")), after, map[string]string{"1": ""}},
		{"other titles are ignored", []any{nov6}, feed(videoEntry{"ccccccccccc", "Rideshare Week 2026", "2026-11-07T06:00:00Z"}), after, map[string]string{"1": ""}},
		{"title prefix in any case", []any{nov6}, feed(videoEntry{"ddddddddddd", "vctc commission meeting", "2026-11-07T06:00:00Z"}), after, map[string]string{"1": "ddddddddddd"}},
		{"between two meetings goes to the only one started", []any{nov6, special}, feed(video("aaaaaaaaaaa", "2026-11-06T06:00:00Z")), after, map[string]string{"1": "", "2": "aaaaaaaaaaa"}},
		{"after both meetings is ambiguous", []any{nov6, special}, feed(video("aaaaaaaaaaa", "2026-11-07T06:00:00Z")), after, map[string]string{"1": "", "2": ""}},
		{"a cancelled meeting gets none", []any{nov6.titled("Commission Meeting – November 2026 (Cancelled)")}, feed(video("aaaaaaaaaaa", "2026-11-07T06:00:00Z")), after, map[string]string{"1": ""}},
		{"clock before the start", []any{nov6}, feed(video("aaaaaaaaaaa", "2026-11-07T06:00:00Z")), time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), map[string]string{"1": ""}},
		{"an unreadable video beside a readable one", []any{nov6}, feed(videoEntry{"bad", "VCTC Commission Meeting - x", "2026-11-07T06:00:00Z"}, video("aaaaaaaaaaa", "2026-11-07T06:00:00Z")), after, map[string]string{"1": "aaaaaaaaaaa"}},
		{"an unreadable video outside the window", []any{nov6}, feed(videoEntry{"bad", "VCTC Commission Meeting - 2020", "2020-01-01T06:00:00Z"}), after, map[string]string{"1": ""}},
	}
	for _, c := range cases {
		got := byID(meetingsOf(t, cfg, posts(t, c.posts...), c.feed, c.now))
		for id, videoID := range c.want {
			m := got[id]
			wantURL := ""
			wantStatus := sources.StatusScheduled
			if videoID != "" {
				wantURL = "https://www.youtube.com/watch?v=" + videoID
				wantStatus = sources.StatusHeld
			}
			if strings.Contains(c.name, "cancelled") {
				wantStatus = sources.StatusCancelled
			}
			if m.RecordingURL != wantURL || m.Status != wantStatus {
				t.Errorf("%s: meeting %s = %s %q, want %s %q", c.name, id, m.Status, m.RecordingURL, wantStatus, wantURL)
			}
		}
	}
}
