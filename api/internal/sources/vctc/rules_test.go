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
	Meta    any    `json:"meta,omitempty"`
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
	got := byID(meetingsOf(t, agendasOnly(), body, nil, now))
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
		func() built {
			b := commission(3, "January 2027", "2027-01-15", "", "")
			b.Meta = map[string]any{"_meeting_date": "2027-01-15", "_meeting_time": 900}
			return b
		}(),
	)
	a, logs := logged(t, agendasOnly(), serving(body, nil), now)
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
		`level=WARN msg="vctc: board meeting post has an unreadable start time; using the configured start time" host=www.goventura.org post=3 time=""`,
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
	a, logs := logged(t, agendasOnly(), serving(body, nil), now)
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
		got := meetingsOf(t, agendasOnly(), posts(t, p.titled(c.title)), nil, now)
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
	a, logs := logged(t, agendasOnly(), serving(body, nil), now)
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
		{"trailing colon", `<h3>Current agenda:</h3><a href="` + uploads + `a.pdf">Agenda</a>`, uploads + "a.pdf"},
		{"agenda alone in capitals", `<h4>AGENDA</h4><a href="` + uploads + `a.pdf">Agenda</a>`, uploads + "a.pdf"},
		{"amended agenda", `<h3>Amended Agenda –</h3><a href="` + uploads + `a.pdf">Agenda</a>`, uploads + "a.pdf"},
		{"id of the real markup", `<h3 id="current-agenda" class="wp-block-heading">Agenda &amp; Notice</h3><a href="` + uploads + `a.pdf">Agenda</a>`, uploads + "a.pdf"},
		{"another heading with agenda in it", `<h3>Agenda Item Attachments</h3><a href="` + uploads + `a.pdf">A</a>`, ""},
		{"empty agenda section then one with a link", `<h3>Current agenda</h3><h3>Amended agenda</h3><a href="` + uploads + `b.pdf">Agenda</a>`, uploads + "b.pdf"},
	}
	for _, c := range cases {
		got := meetingsOf(t, agendasOnly(), posts(t, p.content(c.content)), nil, now)
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
	a, logs := logged(t, agendasOnly(), serving(body, nil), now)
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

func TestMissingAgendaIsLoggedWhenTheMeetingIsNearOrPast(t *testing.T) {
	cases := []struct {
		name  string
		post  built
		now   time.Time
		warns bool
	}{
		{"two weeks ahead", commission(1, "December 2026", "2026-12-04", "09:00", ""), time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), false},
		{"two days ahead", commission(1, "December 2026", "2026-12-04", "09:00", ""), time.Date(2026, 12, 2, 12, 0, 0, 0, time.UTC), true},
		{"past", commission(1, "December 2026", "2026-12-04", "09:00", ""), time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC), true},
		{"past with an agenda", commission(1, "December 2026", "2026-12-04", "09:00", uploads+"a.pdf"), time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC), false},
		{"cancelled", commission(1, "December 2026", "2026-12-04", "09:00", "").titled("Commission Meeting – December 2026 (Cancelled)"), time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC), false},
		{"heading the adapter cannot read", commission(1, "December 2026", "2026-12-04", "09:00", "").content(`<h3>Agenda for the meeting</h3><a href="` + uploads + `a.pdf">Agenda</a>`), time.Date(2026, 12, 2, 12, 0, 0, 0, time.UTC), true},
	}
	for _, c := range cases {
		a, logs := logged(t, agendasOnly(), serving(posts(t, c.post), nil), c.now)
		if _, err := a.Meetings(t.Context(), checkWindow(c.now)); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(logs.String(), `level=WARN msg="vctc: board meeting post has no agenda link though the meeting is near or past"`); got != c.warns {
			t.Errorf("%s: warned %v, want %v:\n%s", c.name, got, c.warns, logs)
		}
	}
}

func TestPresentationsAfterAPastMeetingMeanHeld(t *testing.T) {
	withPresentations := func(b built) built {
		return b.content(agendaSection(uploads+"a.pdf") + `<h3 id="presentations">PRESENTATIONS</h3><a href="` + uploads + `2026/12/2026.12.04_PPT_Item-7.pdf">PPT</a>`)
	}
	cases := []struct {
		name string
		post built
		now  time.Time
		want sources.Status
	}{
		{"past with presentations", withPresentations(commission(1, "December 2026", "2026-12-04", "09:00", "")), time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC), sources.StatusHeld},
		{"presentations before the start", withPresentations(commission(1, "December 2026", "2026-12-04", "09:00", "")), time.Date(2026, 12, 4, 16, 0, 0, 0, time.UTC), sources.StatusScheduled},
		{"cancelled with presentations", withPresentations(commission(1, "December 2026", "2026-12-04", "09:00", "")).titled("Commission Meeting – December 2026 (Cancelled)"), time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC), sources.StatusCancelled},
		{"past without presentations", commission(1, "December 2026", "2026-12-04", "09:00", uploads+"a.pdf"), time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC), sources.StatusScheduled},
		{"empty presentations heading", commission(1, "December 2026", "2026-12-04", "09:00", "").content(agendaSection(uploads+"a.pdf") + `<h3>Presentations</h3>`), time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC), sources.StatusScheduled},
	}
	for _, c := range cases {
		got := meetingsOf(t, agendasOnly(), posts(t, c.post), nil, c.now)
		if len(got) != 1 || got[0].Status != c.want || got[0].RecordingURL != "" {
			t.Errorf("%s: got %+v, want %s with no recording", c.name, got, c.want)
		}
	}
}

func TestPostWithoutAMeetingDateIsDatedFromItsAgendaLink(t *testing.T) {
	november := commission(24950, "November 2026", "2026-11-06", "09:00", uploads+"2026/10/AGENDA-COMMISSION-11.6.26.pdf")
	undated := commission(25100, "December 2026", "", "", "")
	datedByAgenda := commission(25100, "December 2026", "", "", uploads+"2026/11/AGENDA-COMMISSION-12.4.26.pdf")
	inconsistent := commission(25100, "December 2026", "", "", uploads+"2026/11/AGENDA-COMMISSION-11.6.26.pdf")
	twoDates := commission(25100, "December 2026", "", "", uploads+"2026/11/AGENDA-12.4.26-revised-12.11.26.pdf")
	badDate := commission(25100, "December 2026", "2026-12-xx", "10:30", uploads+"2026/11/AGENDA-COMMISSION-12.4.26.pdf")

	stage1 := time.Date(2026, 11, 28, 13, 0, 0, 0, time.UTC)
	stage2 := time.Date(2026, 12, 8, 13, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		now   time.Time
		posts []any
		want  []string
		warn  string
	}{
		{"stage 1: undated beside a dated post", stage1, []any{undated, november}, []string{"24950"}, `level=WARN msg="vctc: skipped a board meeting post" host=www.goventura.org post=25100`},
		{"stage 2: undated alone in the window", stage2, []any{undated, november}, nil, `level=WARN msg="vctc: skipped a board meeting post" host=www.goventura.org post=25100`},
		{"stage 1 with a date in the agenda", stage1, []any{datedByAgenda, november}, []string{"24950", "25100"}, `level=WARN msg="vctc: board meeting post has no meeting date; using the date in its agenda link at the configured start time" host=www.goventura.org post=25100 title="Commission Meeting – December 2026" meeting_date="" source=https://www.goventura.org/wp-content/uploads/2026/11/AGENDA-COMMISSION-12.4.26.pdf date=2026-12-04 start_time=09:00`},
		{"stage 2 with a date in the agenda", stage2, []any{datedByAgenda, november}, []string{"25100"}, "date=2026-12-04"},
		{"agenda date of another month", stage2, []any{inconsistent, november}, nil, `level=WARN msg="vctc: skipped a board meeting post" host=www.goventura.org post=25100`},
		{"two dates of the month", stage2, []any{twoDates, november}, nil, `level=WARN msg="vctc: skipped a board meeting post" host=www.goventura.org post=25100`},
		{"unparseable meta date", stage2, []any{badDate, november}, []string{"25100"}, `meeting_date=2026-12-xx`},
	}
	for _, c := range cases {
		a, logs := logged(t, agendasOnly(), serving(posts(t, c.posts...), nil), c.now)
		got, err := a.Meetings(t.Context(), checkWindow(c.now))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		var ids []string
		for _, m := range got {
			ids = append(ids, m.SourceID)
			if m.SourceID == "25100" && !m.StartsAt.Equal(time.Date(2026, 12, 4, 17, 0, 0, 0, time.UTC)) {
				t.Errorf("%s: 25100 starts at %s, want 09:00 local on 2026-12-04", c.name, m.StartsAt)
			}
		}
		if !slices.Equal(ids, c.want) {
			t.Errorf("%s: meetings %v, want %v", c.name, ids, c.want)
		}
		if !strings.Contains(logs.String(), c.warn) {
			t.Errorf("%s: missing %q in:\n%s", c.name, c.warn, logs)
		}
	}
}

func TestBoardPostWithACommitteeAgendaIsDropped(t *testing.T) {
	now := time.Date(2021, 12, 10, 12, 0, 0, 0, time.UTC)
	transcom := commission(1126, "December 2021", "", "", uploads+"2021/12/AGENDA-PACKET-December-8-2021-TRANSCOM.pdf").titled("Commission Meeting – December 2021 (2)")
	regular := commission(1127, "December 2021", "", "", uploads+"2021/12/VCTC-AGENDA-REPORTS-DECEMBER-3-2021.pdf")
	a, logs := logged(t, agendasOnly(), serving(posts(t, transcom, regular), nil), now)
	got, err := a.Meetings(t.Context(), checkWindow(now))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "1127" || !got[0].StartsAt.Equal(time.Date(2021, 12, 3, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("got %+v, want the December 3 commission meeting alone", got)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="vctc: dropped a board meeting post whose agenda names a committee" host=www.goventura.org post=1126`) {
		t.Errorf("logs:\n%s", logs)
	}
}
