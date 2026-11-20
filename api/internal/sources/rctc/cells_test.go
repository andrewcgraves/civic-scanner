package rctc_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/rctc"
)

const uploads = site + "wp-content/uploads/2026/12/"

const december9 = "DECEMBER 9, 2026 &#8211; 9:30 AM"

var december9At = time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC)

func TestAgendaLinkIsChosenByWhatItIs(t *testing.T) {
	board := linked(site+"december2026agenda", "COMMISSION MEETING")
	cases := map[string]struct {
		cell string
		want string
	}{
		"board link alone":                       {board, site + "december2026agenda"},
		"revised staff report":                   {board + " " + linked(uploads+"Revised-Staff-Report-Item-9.pdf", "REVISED"), site + "december2026agenda"},
		"revised agenda by text":                 {board + " " + linked(uploads+"December-Commission-Rev.pdf", "Revised Agenda"), uploads + "December-Commission-Rev.pdf"},
		"amended agenda by file name":            {board + " " + linked(uploads+"Amended-December-Commission-Agenda.pdf", "AMENDED"), uploads + "Amended-December-Commission-Agenda.pdf"},
		"updated presentation":                   {board + " " + linked(uploads+"Item-7-Presentation.pdf", "Updated Presentation"), site + "december2026agenda"},
		"revised file without the word agenda":   {board + " " + linked(uploads+"Revised-December-Commission.pdf", "REVISED"), site + "december2026agenda"},
		"revised agenda presentation":            {board + " " + linked(uploads+"Revised-Agenda-Presentation.pdf", "Revised Agenda"), site + "december2026agenda"},
		"staff report":                           {board + " " + linked(uploads+"Staff-Report.pdf", "Staff Report"), site + "december2026agenda"},
		"video first":                            {linked("https://cloud.castus.tv/vod/rivcotv/video/1", "VIDEO") + " " + board, site + "december2026agenda"},
		"minutes":                                {board + " " + linked(uploads+"Minutes.pdf", "MINUTES"), site + "december2026agenda"},
		"plain name, agenda link":                {"COMMISSION MEETING " + linked(uploads+"December.pdf", "Agenda"), uploads + "December.pdf"},
		"plain name, revised and first agenda":   {"COMMISSION MEETING " + linked(uploads+"A.pdf", "Agenda") + " " + linked(uploads+"B.pdf", "Revised Agenda"), uploads + "B.pdf"},
		"plain name, minutes only":               {"COMMISSION MEETING " + linked(uploads+"Minutes.pdf", "Minutes"), ""},
		"plain name, agenda item presentation":   {"COMMISSION MEETING " + linked(uploads+"x.pdf", "Agenda Item 9 Presentation"), ""},
		"plain name, unnamed link":               {"COMMISSION MEETING " + linked(uploads+"December-Agenda.pdf", "Click here"), ""},
		"plain name, revised unnamed agenda":     {"COMMISSION MEETING " + linked(uploads+"Revised-December-Agenda.pdf", "Here"), uploads + "Revised-December-Agenda.pdf"},
		"board name pointing at a staff report":  {linked(uploads+"Staff-Report.pdf", "COMMISSION MEETING"), ""},
		"two board links, the first wins":        {board + " " + linked(uploads+"Second.pdf", "COMMISSION MEETING"), site + "december2026agenda"},
		"board link marked revised agenda":       {linked(uploads+"Old.pdf", "Agenda") + " " + linked(uploads+"New.pdf", "COMMISSION MEETING - REVISED AGENDA"), uploads + "New.pdf"},
		"supporting link that says agenda first": {linked(uploads+"Agenda-Item-3-Report.pdf", "Agenda Item 3") + " " + board, site + "december2026agenda"},
	}
	for name, c := range cases {
		m, _ := only(t, page(listed{december9, c.cell}), rulesNow)
		if m.AgendaURL != c.want {
			t.Errorf("%s: agenda %q, want %q", name, m.AgendaURL, c.want)
		}
		if m.SourceID != "commission-meeting/2026-12" {
			t.Errorf("%s: meeting %+v", name, m)
		}
	}
}

func TestBoardIsMatchedOnALinkOfItsOwnWhenTheCellHasSeveral(t *testing.T) {
	board := linked(site+"december2026agenda", "COMMISSION MEETING")
	cells := []string{
		board + " " + linked("https://cloud.castus.tv/vod/rivcotv/video/1", "VIDEO"),
		linked(uploads+"Presentations.pdf", "PRESENTATIONS") + " " + board,
		board + " | " + linked(uploads+"Item-7-Presentation.pdf", "Updated Presentation"),
		board + "<br>" + linked(uploads+"Item-7-Presentation.pdf", "Updated Presentation") + "<br>" + linked(uploads+"Minutes.pdf", "Minutes"),
	}
	for _, cell := range cells {
		body := page(listed{december9, cell}, committee("DECEMBER 21, 2026 &#8211; 10:00 AM"))
		m, logs := only(t, body, rulesNow)
		if m.SourceID != "commission-meeting/2026-12" || m.Status != sources.StatusScheduled || m.AgendaURL != site+"december2026agenda" || !m.StartsAt.Equal(december9At) {
			t.Errorf("%s: %+v", cell, m)
		}
		if len(warnings(logs)) != 0 {
			t.Errorf("%s: warnings:\n%s", cell, logs)
		}
	}

	m, _ := only(t, page(listed{december9, "COMMISSION MEETING " + linked("https://cloud.castus.tv/vod/rivcotv/video/1", "VIDEO")}), rulesNow)
	if m.SourceID != "commission-meeting/2026-12" || m.AgendaURL != "" {
		t.Errorf("plain name with a video link: %+v", m)
	}
}

func TestCancelMarkersBesideTheBoardLink(t *testing.T) {
	board := linked(site+"december2026agenda", "COMMISSION MEETING")
	for _, cell := range []string{
		board + " - CANCELED " + linked(uploads+"Notice.pdf", "NOTICE"),
		board + " " + linked(uploads+"Notice.pdf", "CANCELLED"),
		board + " (POSTPONED) " + linked("https://cloud.castus.tv/vod/rivcotv/video/1", "VIDEO"),
		"COMMISSION MEETING " + linked(uploads+"Notice.pdf", "CANCELED"),
	} {
		m, _ := only(t, page(listed{december9, cell}), rulesNow)
		if m.Status != sources.StatusCancelled || m.SourceID != "commission-meeting/2026-12" {
			t.Errorf("%s: %+v, want cancelled", cell, m)
		}
	}
	m, _ := only(t, page(listed{december9, board + " " + linked(uploads+"Cancellation-Policy.pdf", "Cancellation policy")}), rulesNow)
	if m.Status != sources.StatusScheduled {
		t.Errorf("a link about cancellations cancelled the meeting: %+v", m)
	}
}

func TestTheOnlyCommissionRowWithASecondLinkIsNotMalformed(t *testing.T) {
	body := page(
		committee("DECEMBER 21, 2026 &#8211; 10:00 AM"),
		listed{december9, linked(site+"december2026agenda", "COMMISSION MEETING") + " " + linked("https://cloud.castus.tv/vod/rivcotv/video/1", "VIDEO")},
	)
	a, _ := logged(t, rctc.RCTC, serving(body), rulesNow)
	got, err := a.Meetings(t.Context(), checkWindow(rulesNow))
	if errors.Is(err, rctc.ErrMalformed) || len(got) != 1 {
		t.Errorf("meetings = %v, err = %v; want the commission meeting", got, err)
	}
}

func TestCancellationInTheDateCell(t *testing.T) {
	cases := map[string]time.Time{
		"DECEMBER 9, 2026 &#8211; CANCELED":                 december9At,
		"DECEMBER 9, 2026 &#8211; CANCELLED":                december9At,
		"DECEMBER 9, 2026 (Cancelled)":                      december9At,
		"DECEMBER 9, 2026 &#8211; 9:30 AM &#8211; CANCELED": december9At,
		"DECEMBER 9, 2026 &#8211; 1:30 PM POSTPONED":        time.Date(2026, 12, 9, 21, 30, 0, 0, time.UTC),
		"DECEMBER 9, 2026 &#8211; RESCHEDULED":              december9At,
	}
	for when, want := range cases {
		m, logs := only(t, page(commission(when, "december2026agenda")), rulesNow)
		if m.Status != sources.StatusCancelled || !m.StartsAt.Equal(want) || m.SourceID != "commission-meeting/2026-12" {
			t.Errorf("%q: %+v, want cancelled at %s", when, m, want)
		}
		if len(warnings(logs)) != 0 {
			t.Errorf("%q: warnings:\n%s", when, logs)
		}
	}
}

func TestPostponedAndRescheduledRowsKeepTheirMeeting(t *testing.T) {
	for _, name := range []string{
		"COMMISSION MEETING-POSTPONED",
		"COMMISSION MEETING - POSTPONED",
		"POSTPONED: COMMISSION MEETING",
		"COMMISSION MEETING (RESCHEDULED)",
		"COMMISSION MEETING-CANCELED-POSTPONED",
	} {
		m, logs := only(t, page(listed{december9, linked(site+"december2026agenda", name)}), rulesNow)
		if m.Status != sources.StatusCancelled || m.SourceID != "commission-meeting/2026-12" || m.AgendaURL != site+"december2026agenda" {
			t.Errorf("%q: %+v, want the December meeting, cancelled", name, m)
		}
		if strings.Contains(logs, "not a configured board meeting") {
			t.Errorf("%q: dropped:\n%s", name, logs)
		}
	}
}

func TestAPostponedMeetingGivesItsMonthIDToTheNewDate(t *testing.T) {
	scheduled, _ := only(t, page(commission(december9, "december2026agenda")), rulesNow)
	postponed, _ := only(t, page(listed{december9, linked(site+"december2026agenda", "COMMISSION MEETING-POSTPONED")}), rulesNow)
	if scheduled.SourceID != postponed.SourceID || postponed.Status != sources.StatusCancelled {
		t.Fatalf("postponed = %+v, want the same id as %+v, cancelled", postponed, scheduled)
	}

	body := page(
		listed{december9, linked(site+"december2026agenda", "COMMISSION MEETING-POSTPONED")},
		commission("DECEMBER 16, 2026 &#8211; 9:30 AM", "december2026agenda"),
	)
	got, _ := meetings(t, body, rulesNow, checkWindow(rulesNow))
	want := "" +
		"commission-meeting/2026-12-09 \"Commission Meeting\" 2026-12-09T17:30:00Z cancelled agenda=https://www.rctc.org/december2026agenda\n" +
		"commission-meeting/2026-12 \"Commission Meeting\" 2026-12-16T17:30:00Z scheduled agenda=https://www.rctc.org/december2026agenda\n"
	if d := describe(got); d != want {
		t.Errorf("meetings =\n%s\nwant\n%s", d, want)
	}
}

func TestRescheduledDateForms(t *testing.T) {
	december16 := time.Date(2026, 12, 16, 21, 30, 0, 0, time.UTC)
	cases := map[string]string{
		"<del>DECEMBER 9, 2026</del> DECEMBER 16, 2026 &#8211; 1:30 PM":                           "COMMISSION MEETING",
		"<s>DECEMBER 9, 2026 &#8211; 9:30 AM</s><br>DECEMBER 16, 2026 &#8211; 1:30 PM":            "COMMISSION MEETING",
		"<strike>December 9, 2026</strike> December 16, 2026, 1:30 p.m.":                          "COMMISSION MEETING",
		"DECEMBER 16, 2026 &#8211; <del>9:30 AM</del> 1:30 PM":                                    "COMMISSION MEETING",
		"DECEMBER 16, 2026 &#8211; 1:30 PM (RESCHEDULED FROM DECEMBER 9, 2026)":                   "COMMISSION MEETING",
		"DECEMBER 16, 2026 &#8211; 1:30 PM (Rescheduled from Dec. 9)":                             "COMMISSION MEETING - RESCHEDULED",
		"(RESCHEDULED FROM DECEMBER 9, 2026 &#8211; 9:30 AM) DECEMBER 16, 2026 1:30 PM":           "COMMISSION MEETING",
		"DECEMBER 16, 2026 &#8211; 1:30 PM [MOVED FROM DECEMBER 9, 2026]":                         "COMMISSION MEETING",
		"<del><strong>DECEMBER 9, 2026</strong></del> <strong>DECEMBER 16, 2026</strong> 1:30 PM": "COMMISSION MEETING",
	}
	for when, name := range cases {
		m, logs := only(t, page(listed{when, linked(site+"december2026agenda", name)}), rulesNow)
		if !m.StartsAt.Equal(december16) || m.Status != sources.StatusScheduled || m.SourceID != "commission-meeting/2026-12" {
			t.Errorf("%q (%s): %+v, want scheduled at %s", when, name, m, december16)
		}
		if len(warnings(logs)) != 0 {
			t.Errorf("%q: warnings:\n%s", when, logs)
		}
	}
}

func TestAmbiguousDatesAreStillSkipped(t *testing.T) {
	for _, when := range []string{
		"DECEMBER 9, 2026 DECEMBER 16, 2026",
		"<del>DECEMBER 9, 2026</del>",
		"<del>DECEMBER 9, 2026</del> TBD",
		"DECEMBER 16, 2026 RESCHEDULED FROM DECEMBER 9, 2026",
		"(RESCHEDULED FROM DECEMBER 9, 2026)",
	} {
		body := page(commission("NOVEMBER 10, 2026 &#8211; 9:30 AM", "november2026agenda"), listed{when, linked(site+"december2026agenda", "COMMISSION MEETING")})
		m, logs := only(t, body, rulesNow)
		if m.SourceID != "commission-meeting/2026-11" {
			t.Errorf("%q: %+v", when, m)
		}
		if !strings.Contains(logs, "level=WARN msg=\"rctc: skipped a board meeting without one readable date") {
			t.Errorf("%q: no warning:\n%s", when, logs)
		}
	}
}

func TestAgendaLinksMustBeHTTPSOnTheAgencysDomain(t *testing.T) {
	cases := map[string]string{
		"https://www.rctc.org/december2026agenda":          site + "december2026agenda",
		"https://rctc.org/december2026agenda":              "https://rctc.org/december2026agenda",
		"https://files.rctc.org/December-Agenda.pdf":       "https://files.rctc.org/December-Agenda.pdf",
		"https://WWW.RCTC.ORG/december2026agenda":          "https://WWW.RCTC.ORG/december2026agenda",
		"https://www.rctc.org:443/december2026agenda":      "https://www.rctc.org:443/december2026agenda",
		"/december2026agenda":                              site + "december2026agenda",
		"http://www.rctc.org/december2026agenda":           "",
		"https://evil.example.com/rctc.org/agenda.pdf":     "",
		"https://rctc.org.evil.example.com/agenda.pdf":     "",
		"https://notrctc.org/agenda.pdf":                   "",
		"http://169.254.169.254/latest/meta-data/":         "",
		"https://169.254.169.254/latest/meta-data/":        "",
		"https://localhost/agenda.pdf":                     "",
		"https://127.0.0.1/agenda.pdf":                     "",
		"//evil.example.com/agenda.pdf":                    "",
		"https://www.rctc.org@evil.example.com/agenda.pdf": "",
		"https://user:secret@www.rctc.org/agenda.pdf":      "",
		"https://www.rctc.org:8443/agenda.pdf":             "",
		"ftp://www.rctc.org/agenda.pdf":                    "",
		"javascript:alert(1)":                              "",
		"file:///etc/passwd":                               "",
	}
	for href, want := range cases {
		m, logs := only(t, page(listed{december9, linked(href, "COMMISSION MEETING")}), rulesNow)
		if m.AgendaURL != want || m.SourceID != "commission-meeting/2026-12" {
			t.Errorf("%s: agenda %q, want %q (%+v)", href, m.AgendaURL, want, m)
		}
		dropped := strings.Contains(logs, "level=WARN msg=\"rctc: dropped an agenda link that is not https on the agency's domain")
		if dropped != (want == "") {
			t.Errorf("%s: drop warning %v, want %v:\n%s", href, dropped, want == "", logs)
		}
		if strings.Contains(logs, "secret") {
			t.Errorf("%s: logged the link's password:\n%s", href, logs)
		}
	}
}

func TestRelativeLinksOnAPageServedFromAnotherHostAreDropped(t *testing.T) {
	body := page(listed{december9, linked("/december2026agenda", "COMMISSION MEETING")})
	a, _ := logged(t, rctc.RCTC, redirected(body, "https://mirror.example.com/meetings-and-agendas/"), rulesNow)
	got, err := a.Meetings(t.Context(), checkWindow(rulesNow))
	if err != nil || len(got) != 1 || got[0].AgendaURL != "" {
		t.Errorf("meetings =\n%s\nerr = %v; want the meeting without an agenda", describe(got), err)
	}
}
