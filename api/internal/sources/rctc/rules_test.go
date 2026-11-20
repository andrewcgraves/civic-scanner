package rctc_test

import (
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
)

const site = "https://www.rctc.org/"

var rulesNow = time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)

type listed struct {
	when string
	name string
}

func linked(href, text string) string {
	return `<a href="` + href + `" target="_blank" rel="noopener">` + text + `</a>`
}

func page(rows ...listed) []byte {
	var b strings.Builder
	b.WriteString(`<html><body><table id="currentagenda"><thead><tr><th style="text-align: center;">DATE &amp; TIME</th><th style="text-align: center;">MEETING</th></tr></thead><tbody>`)
	for _, r := range rows {
		b.WriteString(`<tr><td style="text-align: left;">` + r.when + `</td><td style="text-align: left;">` + r.name + `</td></tr>`)
	}
	b.WriteString(`<tr><td></td><td></td></tr></tbody></table></body></html>`)
	return []byte(b.String())
}

func commission(when, slug string) listed {
	return listed{when, linked(site+slug, "COMMISSION MEETING")}
}

func committee(when string) listed {
	return listed{when, linked(site+"november2026tac", "TECHNICAL ADVISORY COMMITTEE")}
}

func only(t *testing.T, body []byte, now time.Time) (sources.Meeting, string) {
	t.Helper()
	got, logs := meetings(t, body, now, checkWindow(now))
	if len(got) != 1 {
		t.Fatalf("meetings =\n%s\nwant one", describe(got))
	}
	return got[0], logs
}

func TestCancelMarkersCancelTheMeetingAndKeepItsID(t *testing.T) {
	for _, name := range []string{
		"COMMISSION MEETING-CANCELED",
		"COMMISSION MEETING - CANCELLED",
		"COMMISSION MEETING (CANCELED)",
		"Commission Meeting – Cancelled",
		"CANCELED - COMMISSION MEETING",
		"**CANCELLED** COMMISSION MEETING",
	} {
		body := page(listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", linked(site+"december2026agenda", name)}, committee("DECEMBER 9, 2026 &#8211; 8:45 AM"))
		m, _ := only(t, body, rulesNow)
		if m.Status != sources.StatusCancelled || m.SourceID != "commission-meeting/2026-12" || m.Title != "Commission Meeting" {
			t.Errorf("%q: %+v, want the cancelled December commission meeting", name, m)
		}
	}
}

func TestCancelWordElsewhereDoesNotMatchTheBoard(t *testing.T) {
	body := page(
		commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"),
		listed{"DECEMBER 10, 2026 &#8211; 9:30 AM", linked(site+"x.pdf", "COMMISSION MEETING CANCELLATION NOTICE")},
	)
	m, logs := only(t, body, rulesNow)
	if m.Status != sources.StatusScheduled {
		t.Errorf("meeting = %+v", m)
	}
	if !strings.Contains(logs, "names the commission but is not a configured board meeting") {
		t.Errorf("the dropped notice was not logged:\n%s", logs)
	}
}

func TestSpecialCommissionMeetingHasADatedID(t *testing.T) {
	body := page(
		commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"),
		listed{"DECEMBER 16, 2026 &#8211; 2:00 PM", linked(site+"wp-content/uploads/2026/12/Special-Commission-Agenda.pdf", "SPECIAL COMMISSION MEETING")},
	)
	got, logs := meetings(t, body, rulesNow, checkWindow(rulesNow))
	want := "" +
		"commission-meeting/2026-12 \"Commission Meeting\" 2026-12-09T17:30:00Z scheduled agenda=https://www.rctc.org/december2026agenda\n" +
		"special-commission-meeting/2026-12-16 \"Special Commission Meeting\" 2026-12-16T22:00:00Z scheduled agenda=https://www.rctc.org/wp-content/uploads/2026/12/Special-Commission-Agenda.pdf\n"
	if d := describe(got); d != want {
		t.Errorf("meetings =\n%s\nwant\n%s", d, want)
	}
	if w := warnings(logs); len(w) != 0 {
		t.Errorf("warnings: %v", w)
	}
}

func TestCommitteesWorkshopsAndCommitteeAgendasAreExcluded(t *testing.T) {
	body := page(
		commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"),
		listed{"DECEMBER 9, 2026 &#8211; 8:45 AM", linked(site+"december2026executive", "EXECUTIVE COMMITTEE")},
		listed{"DECEMBER 14, 2026 &#8211; 9:30 AM", linked(site+"december2026bi", "BUDGET AND IMPLEMENTATION COMMITTEE")},
		listed{"DECEMBER 14, 2026 &#8211; 1:30 PM", linked(site+"december2026wrc", "WESTERN RIVERSIDE COUNTY PROGRAMS AND PROJECTS COMMITTEE")},
		listed{"DECEMBER 15, 2026 &#8211; 11:00 AM", linked(site+"december2026cstac", "CITIZENS AND SPECIALIZED TRANSIT ADVISORY COMMITTEE")},
		listed{"DECEMBER 21, 2026 &#8211; 10:00 AM", linked(site+"december2026tac", "TECHNICAL ADVISORY COMMITTEE")},
		listed{"DECEMBER 2, 2026 &#8211; 9:00 AM", linked(site+"wp-content/uploads/2026/12/Workshop.pdf", "COMMISSION WORKSHOP")},
		listed{"DECEMBER 22, 2026 &#8211; 9:30 AM", linked(site+"wp-content/uploads/2026/12/December-BI-Agenda.pdf", "COMMISSION MEETING")},
		listed{"DECEMBER 23, 2026 &#8211; 9:30 AM", linked(site+"december2026executive", "SPECIAL COMMISSION MEETING")},
		listed{"DECEMBER 28, 2026 &#8211; 9:30 AM", "TOLL POLICY AND OPERATIONS COMMITTEE (COMMISSION)"},
	)
	m, logs := only(t, body, rulesNow)
	if m.SourceID != "commission-meeting/2026-12" {
		t.Errorf("meeting = %+v", m)
	}
	w := warnings(logs)
	if len(w) != 3 {
		t.Fatalf("warnings = %d, want 3 (the workshop and the two committee agendas):\n%s", len(w), strings.Join(w, "\n"))
	}
	for i, want := range []string{"COMMISSION WORKSHOP", "committee=BI", "committee=Executive"} {
		if !strings.Contains(w[i], want) {
			t.Errorf("warning %d = %s, want it to mention %s", i, w[i], want)
		}
	}
}

func TestOnlyExactBoardNamesMatch(t *testing.T) {
	for _, name := range []string{"COMMISSION", "COMMISSION MEETING AND WORKSHOP", "JOINT COMMISSION MEETING", "MEETING"} {
		body := page(commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"), listed{"DECEMBER 10, 2026 &#8211; 9:30 AM", name})
		m, _ := only(t, body, rulesNow)
		if !m.StartsAt.Equal(time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC)) {
			t.Errorf("%q matched: %+v", name, m)
		}
	}
}

func TestNamesAreMatchedWithoutRegardToCaseSpacesEntitiesOrCorrections(t *testing.T) {
	for _, name := range []string{
		"commission   meeting",
		"<strong>COMMISSION</strong><br/>MEETING",
		"COMMISSION&nbsp;MEETING",
		"COMMISSION MEETING - REVISED AGENDA",
		"COMMISSION MEETING (Amended)",
	} {
		body := page(listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", linked(site+"december2026agenda", name)})
		m, _ := only(t, body, rulesNow)
		if m.SourceID != "commission-meeting/2026-12" || m.Status != sources.StatusScheduled {
			t.Errorf("%q: %+v", name, m)
		}
	}
}

func TestStartTimesAcrossDaylightSavingChanges(t *testing.T) {
	cases := []struct {
		when string
		want time.Time
	}{
		{"MARCH 4, 2026 &#8211; 9:30 AM", time.Date(2026, 3, 4, 17, 30, 0, 0, time.UTC)},
		{"MARCH 8, 2026 &#8211; 9:30 AM", time.Date(2026, 3, 8, 16, 30, 0, 0, time.UTC)},
		{"MARCH 11, 2026 &#8211; 9:30 AM", time.Date(2026, 3, 11, 16, 30, 0, 0, time.UTC)},
		{"OCTOBER 28, 2026 &#8211; 9:30 AM", time.Date(2026, 10, 28, 16, 30, 0, 0, time.UTC)},
		{"NOVEMBER 1, 2026 &#8211; 9:30 AM", time.Date(2026, 11, 1, 17, 30, 0, 0, time.UTC)},
		{"NOVEMBER 4, 2026 &#8211; 9:30 AM", time.Date(2026, 11, 4, 17, 30, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		body := page(listed{c.when, "COMMISSION MEETING"})
		window := sources.Window{From: c.want.AddDate(0, 0, -1), To: c.want.AddDate(0, 0, 1)}
		got, _ := meetings(t, body, c.want.AddDate(0, 0, 10), window)
		if len(got) != 1 || !got[0].StartsAt.Equal(c.want) {
			t.Errorf("%s: %s, want start %s", c.when, describe(got), c.want)
		}
	}
}

func TestTimeForms(t *testing.T) {
	cases := map[string]time.Time{
		"DECEMBER 9, 2026 &#8211; 9:30 AM":               time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC),
		"December 9, 2026 - 9:30 a.m.":                   time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC),
		"WEDNESDAY, DECEMBER 9, 2026 AT 2 PM":            time.Date(2026, 12, 9, 22, 0, 0, 0, time.UTC),
		"Dec. 9th, 2026 @ 1:30PM":                        time.Date(2026, 12, 9, 21, 30, 0, 0, time.UTC),
		"12/9/2026 &#8211; 12:00 PM":                     time.Date(2026, 12, 9, 20, 0, 0, 0, time.UTC),
		"DECEMBER&nbsp;9,&#160;2026 &#8211;&nbsp;NOON":   time.Date(2026, 12, 9, 20, 0, 0, 0, time.UTC),
		"  DECEMBER 9,   2026<br>\n 9:30 AM  ":           time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC),
		"SEPTEMBER 9, 2026 &#8211; 9:30 AM (REVISED)":    time.Date(2026, 9, 9, 16, 30, 0, 0, time.UTC),
		"<strong>JANUARY 13, 2027</strong> – 9:30 AM":    time.Date(2027, 1, 13, 17, 30, 0, 0, time.UTC),
		"FEBRUARY 10, 2027 &#8211; 12:15 AM":             time.Date(2027, 2, 10, 8, 15, 0, 0, time.UTC),
		"DECEMBER 9, 2026, 9:30 AM &#8211; 11:00 AM":     time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC),
		"DECEMBER 9 2026 9:30AM":                         time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC),
		"Wed., Dec 9, 2026 &#8211; 9:30 a. m.":           time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC),
		"SEPTEMBER 30, 2026 &#8211; 9:30 AM&nbsp;&nbsp;": time.Date(2026, 9, 30, 16, 30, 0, 0, time.UTC),
	}
	for when, want := range cases {
		body := page(listed{when, "COMMISSION MEETING"})
		window := sources.Window{From: want.AddDate(0, 0, -1), To: want.AddDate(0, 0, 1)}
		got, logs := meetings(t, body, rulesNow, window)
		if len(got) != 1 || !got[0].StartsAt.Equal(want) {
			t.Errorf("%q: %s, want start %s", when, describe(got), want)
		}
		if strings.Contains(logs, "start time") {
			t.Errorf("%q: logged a fallback start time:\n%s", when, logs)
		}
	}
}

func TestMissingOrUnreadableTimeUsesTheConfiguredStart(t *testing.T) {
	cases := map[string]string{
		"DECEMBER 9, 2026":                  "level=INFO msg=\"rctc: board meeting has no start time",
		"DECEMBER 9, 2026 &#8211; ":         "level=INFO msg=\"rctc: board meeting has no start time",
		"DECEMBER 9, 2026 &#8211; TBD":      "level=WARN msg=\"rctc: board meeting has an unreadable start time",
		"DECEMBER 9, 2026 &#8211; 9:30":     "level=WARN msg=\"rctc: board meeting has an unreadable start time",
		"DECEMBER 9, 2026 &#8211; 13:75 PM": "level=WARN msg=\"rctc: board meeting has an unreadable start time",
	}
	for when, wantLog := range cases {
		m, logs := only(t, page(listed{when, "COMMISSION MEETING"}), rulesNow)
		if !m.StartsAt.Equal(time.Date(2026, 12, 9, 17, 30, 0, 0, time.UTC)) {
			t.Errorf("%q: start %s, want 09:30 local", when, m.StartsAt)
		}
		if !strings.Contains(logs, wantLog) {
			t.Errorf("%q: logs lack %s:\n%s", when, wantLog, logs)
		}
	}
}

func TestOldRowsOutsideTheWindowLogOnlyAtDebug(t *testing.T) {
	body := page(
		commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"),
		listed{"MAY 13, 2026", linked(site+"may2026agenda", "COMMISSION MEETING")},
		listed{"JUNE 10, 2026 &#8211; TBD", linked(site+"june2026agenda", "COMMISSION MEETING")},
		listed{"TBD", linked(site+"july2026agenda", "COMMISSION MEETING")},
		listed{"AUGUST 12, 2026 &#8211; 9:30 AM", "COMMISSION MEETING"},
		listed{"AUGUST 20, 2026 &#8211; 9:30 AM", "COMMISSION WORKSHOP"},
		listed{"SEPTEMBER 9, 2026 &#8211; 9:30 AM", linked(site+"september2026executive", "COMMISSION MEETING")},
		listed{"OCTOBER 14, 2025 &#8211; 9:30 AM", linked(site+"october2026agenda", "COMMISSION MEETING")},
	)
	_, logs := only(t, body, rulesNow)
	if strings.Contains(logs, "level=WARN") || strings.Contains(logs, "level=INFO") {
		t.Errorf("rows outside the window logged above debug:\n%s", logs)
	}
}

func TestUndatedRowsAreSkippedNeverGuessed(t *testing.T) {
	cases := map[string]listed{
		"no date":          {"TBD", linked(site+"december2026agenda", "COMMISSION MEETING")},
		"month only":       {"DECEMBER 2026 &#8211; 9:30 AM", linked(site+"december2026agenda", "COMMISSION MEETING")},
		"two dates":        {"DECEMBER 9, 2026 OR DECEMBER 16, 2026", linked(site+"december2026agenda", "COMMISSION MEETING")},
		"impossible date":  {"FEBRUARY 30, 2027 &#8211; 9:30 AM", linked(site+"february2027agenda", "COMMISSION MEETING")},
		"empty date, link": {"", linked(site+"december2026agenda", "COMMISSION MEETING")},
	}
	for name, row := range cases {
		body := page(commission("NOVEMBER 10, 2026 &#8211; 9:30 AM", "november2026agenda"), row)
		m, logs := only(t, body, rulesNow)
		if m.SourceID != "commission-meeting/2026-11" {
			t.Errorf("%s: meeting = %+v", name, m)
		}
		if !strings.Contains(logs, "level=WARN msg=\"rctc: skipped a board meeting without one readable date") {
			t.Errorf("%s: no warning for the skipped row:\n%s", name, logs)
		}
	}
}

func TestAgendaLinkMonthDisagreeingWithTheDateIsLogged(t *testing.T) {
	body := page(commission("DECEMBER 9, 2025 &#8211; 9:30 AM", "december2026agenda"), commission("DECEMBER 10, 2026 &#8211; 9:30 AM", "december2026agenda"))
	now := time.Date(2025, 12, 1, 13, 0, 0, 0, time.UTC)
	m, logs := only(t, body, now)
	if !m.StartsAt.Equal(time.Date(2025, 12, 9, 17, 30, 0, 0, time.UTC)) || m.SourceID != "commission-meeting/2025-12" {
		t.Errorf("meeting = %+v, want the stated date", m)
	}
	if !strings.Contains(logs, "level=WARN msg=\"rctc: the agenda link names another month than the meeting date") {
		t.Errorf("no warning:\n%s", logs)
	}
}

func TestNoAgendaYet(t *testing.T) {
	body := page(listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", "COMMISSION MEETING"})
	m, logs := only(t, body, rulesNow)
	if m.AgendaURL != "" || m.Status != sources.StatusScheduled || m.PageURL != pageURL {
		t.Errorf("meeting = %+v", m)
	}
	if len(warnings(logs)) != 0 {
		t.Errorf("warned about an agenda due in three weeks:\n%s", logs)
	}

	near := time.Date(2026, 12, 7, 13, 0, 0, 0, time.UTC)
	_, logs = only(t, body, near)
	if !strings.Contains(logs, "level=WARN msg=\"rctc: board meeting has no agenda link though the meeting is near or past") {
		t.Errorf("no warning two days before the meeting:\n%s", logs)
	}

	cancelled := page(listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", "COMMISSION MEETING-CANCELED"})
	_, logs = only(t, cancelled, near)
	if len(warnings(logs)) != 0 {
		t.Errorf("warned about a cancelled meeting's agenda:\n%s", logs)
	}
}

func TestPastAndFutureMeetingsAreScheduledUntilRecordingsLand(t *testing.T) {
	body := page(
		commission("NOVEMBER 10, 2026 &#8211; 9:30 AM", "november2026agenda"),
		commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"),
		commission("JANUARY 13, 2027 &#8211; 9:30 AM", "january2027agenda"),
		commission("MARCH 10, 2027 &#8211; 9:30 AM", "march2027agenda"),
	)
	got, _ := meetings(t, body, rulesNow, checkWindow(rulesNow))
	want := "" +
		"commission-meeting/2026-11 \"Commission Meeting\" 2026-11-10T17:30:00Z scheduled agenda=https://www.rctc.org/november2026agenda\n" +
		"commission-meeting/2026-12 \"Commission Meeting\" 2026-12-09T17:30:00Z scheduled agenda=https://www.rctc.org/december2026agenda\n" +
		"commission-meeting/2027-01 \"Commission Meeting\" 2027-01-13T17:30:00Z scheduled agenda=https://www.rctc.org/january2027agenda\n"
	if d := describe(got); d != want {
		t.Errorf("meetings =\n%s\nwant\n%s", d, want)
	}
	for _, m := range got {
		if m.RecordingURL != "" {
			t.Errorf("%s has a recording %s", m.SourceID, m.RecordingURL)
		}
	}
}

func TestSourceIDSurvivesTitleEditsAndReschedulesWithinTheMonth(t *testing.T) {
	states := [][]byte{
		page(listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", "COMMISSION MEETING"}),
		page(commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda")),
		page(listed{"DECEMBER 16, 2026 &#8211; 1:30 PM", linked(site+"december2026agenda", "COMMISSION MEETING - REVISED AGENDA")}),
		page(listed{"DECEMBER 16, 2026 &#8211; 1:30 PM", linked(site+"december2026agenda", "COMMISSION MEETING-CANCELED")}),
		page(committee("NOVEMBER 16, 2026 &#8211; 10:00 AM"), listed{"DECEMBER 16, 2026", linked(site+"december2026agenda", "Commission Meeting")}),
	}
	for i, body := range states {
		m, _ := only(t, body, rulesNow)
		if m.SourceID != "commission-meeting/2026-12" {
			t.Errorf("state %d: id %s, want commission-meeting/2026-12", i, m.SourceID)
		}
	}
}

func TestTwoRegularMeetingsInOneMonthGetDistinctIDs(t *testing.T) {
	body := page(
		commission("DECEMBER 16, 2026 &#8211; 9:30 AM", "december2026agenda2"),
		commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"),
	)
	got, logs := meetings(t, body, rulesNow, checkWindow(rulesNow))
	if len(got) != 2 || got[0].SourceID != "commission-meeting/2026-12" || got[1].SourceID != "commission-meeting/2026-12-16" {
		t.Errorf("meetings =\n%s", describe(got))
	}
	if !strings.Contains(logs, "already has the usual id") {
		t.Errorf("no warning:\n%s", logs)
	}
}

func TestTwoSpecialMeetingsOnOneDayGetDistinctIDs(t *testing.T) {
	body := page(
		listed{"DECEMBER 16, 2026 &#8211; 2:00 PM", "SPECIAL COMMISSION MEETING"},
		listed{"DECEMBER 16, 2026 &#8211; 9:00 AM", "SPECIAL COMMISSION MEETING"},
	)
	got, _ := meetings(t, body, rulesNow, checkWindow(rulesNow))
	if len(got) != 2 || got[0].SourceID != "special-commission-meeting/2026-12-16" || got[1].SourceID != "special-commission-meeting/2026-12-16T1400" {
		t.Errorf("meetings =\n%s", describe(got))
	}
}

func TestADuplicatedRowIsOneMeeting(t *testing.T) {
	body := page(
		listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", "COMMISSION MEETING"},
		commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"),
	)
	m, logs := only(t, body, rulesNow)
	if m.AgendaURL != site+"december2026agenda" {
		t.Errorf("meeting = %+v, want the agenda of the second row", m)
	}
	if !strings.Contains(logs, "lists one board meeting twice") {
		t.Errorf("no warning:\n%s", logs)
	}
}

func TestAgendaLinks(t *testing.T) {
	cases := map[string]struct {
		cell string
		want string
		warn bool
	}{
		"short link":    {linked(site+"december2026agenda", "COMMISSION MEETING"), site + "december2026agenda", false},
		"direct pdf":    {linked(site+"wp-content/uploads/2026/12/December-Commission-Agenda.pdf", "COMMISSION MEETING"), site + "wp-content/uploads/2026/12/December-Commission-Agenda.pdf", false},
		"relative":      {linked("/december2026agenda", "COMMISSION MEETING"), site + "december2026agenda", false},
		"page relative": {linked("../wp-content/uploads/2026/12/a.pdf#page=2", "COMMISSION MEETING"), site + "wp-content/uploads/2026/12/a.pdf", false},
		"entities":      {`<a href="https://www.rctc.org/december2026agenda?a=1&amp;b=2">COMMISSION MEETING</a>`, site + "december2026agenda?a=1&b=2", false},
		"spaces":        {`<a href="  https://www.rctc.org/december2026agenda  ">COMMISSION MEETING</a>`, site + "december2026agenda", false},
		"mailto":        {linked("mailto:info@rctc.org", "COMMISSION MEETING"), "", true},
		"bad url":       {linked("http://[::1", "COMMISSION MEETING"), "", true},
		"revised wins":  {linked(site+"december2026agenda", "COMMISSION MEETING") + " " + linked(site+"wp-content/uploads/2026/12/Revised-December-Agenda.pdf", "REVISED"), site + "wp-content/uploads/2026/12/Revised-December-Agenda.pdf", false},
		"empty href":    {`<a href="">COMMISSION MEETING</a>`, "", false},
	}
	for name, c := range cases {
		body := page(listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", c.cell})
		m, logs := only(t, body, rulesNow)
		if m.AgendaURL != c.want {
			t.Errorf("%s: agenda %q, want %q", name, m.AgendaURL, c.want)
		}
		if got := len(warnings(logs)) > 0; got != c.warn {
			t.Errorf("%s: warned %v, want %v:\n%s", name, got, c.warn, logs)
		}
	}
}

func TestRedirectedPageResolvesRelativeLinksAgainstTheFinalURL(t *testing.T) {
	body := page(listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", linked("agenda.pdf", "COMMISSION MEETING")})
	a, _ := logged(t, rctcConfig(), redirected(body, "https://www.rctc.org/meetings/"), rulesNow)
	got, err := a.Meetings(t.Context(), checkWindow(rulesNow))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AgendaURL != "https://www.rctc.org/meetings/agenda.pdf" || got[0].PageURL != pageURL {
		t.Errorf("meetings =\n%s", describe(got))
	}
}

func TestMeetingsOutsideTheWindowAreNotReturned(t *testing.T) {
	body := fixture(t, "rctc_meetings_and_agendas.html")
	now := time.Date(2027, 3, 1, 13, 0, 0, 0, time.UTC)
	got, _ := meetings(t, body, now, checkWindow(now))
	if len(got) != 0 {
		t.Errorf("meetings =\n%s\nwant none in the window", describe(got))
	}
}

func TestTablesWithOtherColumnsOrderAndExtraCells(t *testing.T) {
	body := []byte(`<table><tr><th>Meeting</th><th>Location</th><th>Date and time</th></tr>` +
		`<tr><td><a href="/december2026agenda">COMMISSION MEETING</a></td><td>Board Room</td><td>DECEMBER 9, 2026 – 9:30 AM</td></tr>` +
		`<tr><td>COMMISSION MEETING</td></tr>` +
		`<tr><td>SPECIAL COMMISSION MEETING<td>Board Room<td>DECEMBER 18, 2026 9 AM</table>`)
	got, _ := meetings(t, body, rulesNow, checkWindow(rulesNow))
	want := "" +
		"commission-meeting/2026-12 \"Commission Meeting\" 2026-12-09T17:30:00Z scheduled agenda=https://www.rctc.org/december2026agenda\n" +
		"special-commission-meeting/2026-12-18 \"Special Commission Meeting\" 2026-12-18T17:00:00Z scheduled agenda=\n"
	if d := describe(got); d != want {
		t.Errorf("meetings =\n%s\nwant\n%s", d, want)
	}
}
