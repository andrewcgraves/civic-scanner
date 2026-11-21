package rctc_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/rctc"
)

type video struct {
	n        int
	title    string
	uploaded string
	access   string
}

func videoID(n int) string {
	return fmt.Sprintf("6af6%020x", n)
}

func mediaURL(n int) string {
	return "https://dlttx48mxf9m3.cloudfront.net/outputs/" + videoID(n) + "/Default/HLS/out.m3u8"
}

func listing(t *testing.T, vs ...video) []byte {
	t.Helper()
	items := make([]map[string]any, len(vs))
	for i, v := range vs {
		access := v.access
		if access == "" {
			access = "open"
		}
		uploaded := v.uploaded
		if uploaded == "" {
			uploaded = "2026-11-12T20:46:03.191Z"
		}
		items[i] = map[string]any{
			"_id":      videoID(v.n),
			"date":     uploaded,
			"access":   access,
			"metadata": map[string]any{"filename": v.title, "duration": "2946.33"},
		}
	}
	body, err := json.Marshal(map[string]any{"_id": "61290f1a4508fb00087c09db", "name": "Riverside County Transportation Commission", "videos": items})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

const november10 = "NOVEMBER 10, 2026 &#8211; 9:30 AM"

func withVideos(t *testing.T, body, listed []byte, now time.Time, window sources.Window) ([]sources.Meeting, string) {
	t.Helper()
	a, buf := logged(t, rctc.RCTC, servingWith(body, listed), now)
	got, err := a.Meetings(t.Context(), window)
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	return got, buf.String()
}

func TestRecordedListingGivesEachPastCommissionMeetingItsVideo(t *testing.T) {
	year := sources.Window{From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	got, logs := withVideos(t, fixture(t, "rctc_meetings_and_agendas.html"), fixture(t, "castus_rctc_playlist.json"), recordedAt, year)
	const media = "https://dlttx48mxf9m3.cloudfront.net/outputs/"
	want := "" +
		"commission-meeting/2026-05 \"Commission Meeting\" 2026-05-13T16:30:00Z held agenda=https://www.rctc.org/may2026agenda\n" +
		"  recording=" + media + "6a04c5b436987380b1cf72b5/Default/HLS/out.m3u8\n" +
		"commission-meeting/2026-06 \"Commission Meeting\" 2026-06-10T16:30:00Z held agenda=https://www.rctc.org/june2026agenda\n" +
		"  recording=" + media + "6a2b219b89c381fbbab6ecaf/Default/HLS/out.m3u8\n" +
		"commission-meeting/2026-08 \"Commission Meeting\" 2026-08-12T16:30:00Z held agenda=https://www.rctc.org/august2026agenda\n" +
		"  recording=" + media + "6a8502419c2a6f75355d31ea/Default/HLS/out.m3u8\n" +
		"commission-meeting/2026-09 \"Commission Meeting\" 2026-09-09T16:30:00Z held agenda=https://www.rctc.org/september2026agenda\n" +
		"  recording=" + media + "6ab30350bd40c90801496ac0/Default/HLS/out.m3u8\n" +
		"commission-meeting/2026-10 \"Commission Meeting\" 2026-10-14T16:30:00Z scheduled agenda=https://www.rctc.org/october2026agenda\n" +
		"commission-meeting/2026-11 \"Commission Meeting\" 2026-11-10T17:30:00Z held agenda=https://www.rctc.org/november2026agenda\n" +
		"  recording=" + media + "6af62b4f05fc227eedc662d8/Default/HLS/out.m3u8\n"
	if d := describe(got); d != want {
		t.Errorf("meetings =\n%s\nwant\n%s", d, want)
	}
	w := warnings(logs)
	if len(w) != 2 || !strings.Contains(w[0], "date=2026-03-11") || !strings.Contains(w[1], "date=2026-02-11") ||
		!strings.Contains(w[0], "matches no board meeting") || !strings.Contains(w[1], "matches no board meeting") {
		t.Errorf("warnings = %q, want the February and March videos, whose meetings are not on the page", w)
	}
}

func TestCommissionTitlesWithOddSpacingCaseOrUnpaddedDatesMatch(t *testing.T) {
	cases := map[string]string{
		"RCTC General Commission 11/10/2026 Meeting":     november10,
		"RCTC General Commission  11/10/2026 Meeting":    november10,
		" RCTC General Commission 11/10/2026  Meeting  ": november10,
		"rctc general commission 11/10/2026 meeting":     november10,
		"RCTC General Commission 11/3/2026 Meeting":      "NOVEMBER 3, 2026 &#8211; 9:30 AM",
	}
	for title, when := range cases {
		got, logs := withVideos(t, page(commission(when, "x2026agenda")), listing(t, video{n: 1, title: title}), rulesNow, checkWindow(rulesNow))
		if len(got) != 1 || got[0].RecordingURL != mediaURL(1) || got[0].Status != sources.StatusHeld {
			t.Errorf("%q: meetings =\n%s", title, describe(got))
		}
		if w := warnings(logs); len(w) != 0 {
			t.Errorf("%q: warnings %q", title, w)
		}
	}
}

func TestTheVideoDateComesFromTheTitleNotTheUpload(t *testing.T) {
	body := page(commission(november10, "november2026agenda"))
	got, _ := withVideos(t, body, listing(t, video{n: 1, title: "RCTC General Commission 11/10/2026 Meeting", uploaded: "2026-12-20T10:00:00.000Z"}), rulesNow, checkWindow(rulesNow))
	if len(got) != 1 || got[0].RecordingURL != mediaURL(1) {
		t.Errorf("meetings =\n%s", describe(got))
	}
}

func TestUnreadableCommissionTitlesAreSkipped(t *testing.T) {
	body := page(commission(november10, "november2026agenda"))
	listed := listing(t,
		video{n: 1, title: "RCTC General Commission 11/10/2026 Meeting"},
		video{n: 2, title: "RCTC General Commission November 2026 Meeting", uploaded: "2026-11-15T10:00:00.000Z"},
		video{n: 3, title: "RCTC General Commission 13/40/2026 Meeting", uploaded: "not a date"},
		video{n: 4, title: "RCTC General Commission Meeting", uploaded: "2022-03-09T19:15:59.639Z"},
	)
	got, logs := withVideos(t, body, listed, rulesNow, checkWindow(rulesNow))
	if len(got) != 1 || got[0].RecordingURL != mediaURL(1) {
		t.Errorf("meetings =\n%s", describe(got))
	}
	w := warnings(logs)
	if len(w) != 2 || !strings.Contains(w[0], videoID(2)) || !strings.Contains(w[1], videoID(3)) {
		t.Errorf("warnings = %q, want the two recent unreadable titles", w)
	}
	if !strings.Contains(logs, "level=DEBUG msg=\"rctc: skipped a commission video whose title has no readable date\" page="+pageURL+" video="+videoID(4)) {
		t.Errorf("the old unreadable title was not logged at debug:\n%s", logs)
	}
}

func TestCommitteeVideosAreIgnored(t *testing.T) {
	body := page(commission(november10, "november2026agenda"))
	listed := listing(t,
		video{n: 1, title: "RCTC Budget & Implementation 11/10/2026 Meeting"},
		video{n: 2, title: "RCTC Western Riverside County Programs and Projects 11/10/2026 Meeting"},
		video{n: 3, title: "RCTC General Commission 05/13/2026 Meeting"},
	)
	got, logs := withVideos(t, body, listed, rulesNow, checkWindow(rulesNow))
	if len(got) != 1 || got[0].RecordingURL != "" || got[0].Status != sources.StatusScheduled {
		t.Errorf("meetings =\n%s", describe(got))
	}
	if w := warnings(logs); len(w) != 0 {
		t.Errorf("warnings = %q", w)
	}
}

func TestVideosThatCannotBeMatchedToOneMeetingGiveNoRecording(t *testing.T) {
	cases := []struct {
		name   string
		body   []byte
		videos []video
		status sources.Status
		warn   string
	}{
		{
			name:   "two videos on one date",
			body:   page(commission(november10, "november2026agenda")),
			videos: []video{{n: 1, title: "RCTC General Commission 11/10/2026 Meeting"}, {n: 2, title: "RCTC General Commission  11/10/2026 Meeting"}},
			status: sources.StatusScheduled,
			warn:   "several commission videos share one date",
		},
		{
			name:   "a video with no meeting",
			body:   page(commission(november10, "november2026agenda")),
			videos: []video{{n: 1, title: "RCTC General Commission 11/12/2026 Meeting"}},
			status: sources.StatusScheduled,
			warn:   "matches no board meeting on the page",
		},
		{
			name:   "a regular and a special meeting on the date",
			body:   page(commission(november10, "november2026agenda"), listed{"NOVEMBER 10, 2026 &#8211; 1:00 PM", linked(site+"special-agenda.pdf", "SPECIAL COMMISSION MEETING")}),
			videos: []video{{n: 1, title: "RCTC General Commission 11/10/2026 Meeting"}},
			status: sources.StatusScheduled,
			warn:   "matches several board meetings on one date",
		},
		{
			name:   "a cancelled meeting",
			body:   page(listed{november10, linked(site+"november2026agenda", "COMMISSION MEETING-CANCELED")}),
			videos: []video{{n: 1, title: "RCTC General Commission 11/10/2026 Meeting"}},
			status: sources.StatusCancelled,
			warn:   "matches a cancelled board meeting",
		},
		{
			name:   "a meeting that has not started",
			body:   page(commission("NOVEMBER 19, 2026 &#8211; 9:30 AM", "november2026agenda")),
			videos: []video{{n: 1, title: "RCTC General Commission 11/19/2026 Meeting"}},
			status: sources.StatusScheduled,
			warn:   "matches a board meeting that has not started",
		},
	}
	for _, c := range cases {
		got, logs := withVideos(t, c.body, listing(t, c.videos...), rulesNow, checkWindow(rulesNow))
		for _, m := range got {
			if m.RecordingURL != "" || (m.Title == "Commission Meeting" && m.Status != c.status) {
				t.Errorf("%s: meetings =\n%s", c.name, describe(got))
			}
		}
		if w := warnings(logs); len(w) != 1 || !strings.Contains(w[0], c.warn) {
			t.Errorf("%s: warnings = %q, want one about %q", c.name, w, c.warn)
		}
	}
}

func TestUnmatchedVideosOutsideTheWindowAreNotWarned(t *testing.T) {
	body := page(commission(november10, "november2026agenda"))
	listed := listing(t,
		video{n: 1, title: "RCTC General Commission 11/10/2026 Meeting"},
		video{n: 2, title: "RCTC General Commission 12/8/2021 Meeting"},
		video{n: 3, title: "RCTC General Commission 03/09/2022 Meeting"},
		video{n: 4, title: "RCTC General Commission 03/09/2022 Meeting"},
	)
	got, logs := withVideos(t, body, listed, rulesNow, checkWindow(rulesNow))
	if len(got) != 1 || got[0].RecordingURL != mediaURL(1) {
		t.Errorf("meetings =\n%s", describe(got))
	}
	if w := warnings(logs); len(w) != 0 {
		t.Errorf("warnings = %q", w)
	}
}

func TestPrivateOrOddlyNamedVideosAreSkipped(t *testing.T) {
	body := page(commission(november10, "november2026agenda"))
	cases := map[string][]byte{
		"private": listing(t, video{n: 1, title: "RCTC General Commission 11/10/2026 Meeting", access: "private"}),
		"bad id":  []byte(`{"videos":[{"_id":"../../x","date":"2026-11-12T20:46:03.191Z","access":"open","metadata":{"filename":"RCTC General Commission 11/10/2026 Meeting"}}]}`),
	}
	for name, listed := range cases {
		got, logs := withVideos(t, body, listed, rulesNow, checkWindow(rulesNow))
		if len(got) != 1 || got[0].RecordingURL != "" || got[0].Status != sources.StatusScheduled {
			t.Errorf("%s: meetings =\n%s", name, describe(got))
		}
		if w := warnings(logs); len(w) != 1 || !strings.Contains(w[0], "skipped a commission video") {
			t.Errorf("%s: warnings = %q", name, w)
		}
	}
}

func TestListingShapeChangesAreMalformed(t *testing.T) {
	body := fixture(t, "rctc_meetings_and_agendas.html")
	cases := map[string]string{
		"empty body":            "",
		"html":                  "<html><body>Not found</body></html>",
		"array":                 `[{"_id":"6af62b4f05fc227eedc662d8"}]`,
		"no videos":             `{"_id":"61290f1a4508fb00087c09db","name":"Riverside County Transportation Commission"}`,
		"null videos":           `{"videos":null}`,
		"videos not an array":   `{"videos":"none"}`,
		"videos not objects":    `{"videos":[1,"two",null]}`,
		"title field renamed":   `{"videos":[{"_id":"6af62b4f05fc227eedc662d8","access":"open","metadata":{"title":"RCTC General Commission 11/10/2026 Meeting"}}]}`,
		"no title has a date":   string(listing(t, video{n: 1, title: "RCTC General Commission Meeting"}, video{n: 2, title: "RCTC General Commission 2026-11-10"})),
		"only committee videos": string(listing(t, video{n: 1, title: "RCTC Budget & Implementation 11/10/2026 Meeting"})),
	}
	for name, listed := range cases {
		a, _ := logged(t, rctc.RCTC, servingWith(body, []byte(listed)), recordedAt)
		got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
		if !errors.Is(err, rctc.ErrMalformed) || sources.Retryable(err) || got != nil {
			t.Errorf("%s: meetings %v, err = %v; want permanent ErrMalformed", name, got, err)
		}
	}
}

func TestListingFetchErrorsKeepTheirRetryability(t *testing.T) {
	body := fixture(t, "rctc_meetings_and_agendas.html")
	cases := []struct {
		err       *fetch.Error
		retryable bool
	}{
		{&fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}, true},
		{&fetch.Error{StatusCode: 403, Err: errors.New("forbidden")}, false},
	}
	for _, c := range cases {
		fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
			if req.URL == listingURL {
				return fetch.Response{}, c.err
			}
			return fetch.Response{Status: 200, ContentType: "text/html", Body: body, FinalURL: req.URL}, nil
		}}
		a, _ := logged(t, rctc.RCTC, fake, recordedAt)
		_, err := a.Meetings(t.Context(), checkWindow(recordedAt))
		if !errors.Is(err, c.err) || sources.Retryable(err) != c.retryable {
			t.Errorf("err = %v, retryable %v; want %v wrapped, retryable %v", err, sources.Retryable(err), c.err, c.retryable)
		}
	}
}

func TestWithoutCastusTheListingIsNotFetched(t *testing.T) {
	cfg := rctc.RCTC
	cfg.Castus = rctc.Castus{}
	fake := servingWith(fixture(t, "rctc_meetings_and_agendas.html"), []byte("must not be read"))
	a, _ := logged(t, cfg, fake, recordedAt)
	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil || len(got) != 1 || got[0].RecordingURL != "" || got[0].Status != sources.StatusScheduled {
		t.Errorf("meetings = %v, err = %v", got, err)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].URL != pageURL {
		t.Errorf("requests = %+v, want the page only", calls)
	}
}
