package rctc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/rctc"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

func rctcConfig() rctc.Config {
	return rctc.RCTC
}

func redirected(body []byte, final string) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(fetch.Request) (fetch.Response, error) {
		return fetch.Response{Status: 200, ContentType: "text/html", Body: body, FinalURL: final}, nil
	}}
}

func TestFetchErrorsKeepTheirRetryability(t *testing.T) {
	cases := []struct {
		name      string
		err       *fetch.Error
		retryable bool
	}{
		{"server error", &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}, true},
		{"not found", &fetch.Error{StatusCode: 404, Err: errors.New("not found")}, false},
		{"too large", &fetch.Error{Err: fetch.ErrTooLarge}, false},
	}
	for _, c := range cases {
		a, _ := logged(t, rctc.RCTC, fetchtest.Failing(c.err), rulesNow)
		_, err := a.Meetings(t.Context(), checkWindow(rulesNow))
		if !errors.Is(err, c.err) || sources.Retryable(err) != c.retryable {
			t.Errorf("%s: err = %v, retryable %v; want %v wrapped, retryable %v", c.name, err, sources.Retryable(err), c.err, c.retryable)
		}
	}
}

func TestCanceledContextIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a, _ := logged(t, rctc.RCTC, serving(page(commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"))), rulesNow)
	if _, err := a.Meetings(ctx, checkWindow(rulesNow)); !sources.Retryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}

func TestPageShapeChangesAreMalformed(t *testing.T) {
	cases := map[string]string{
		"empty body":          "",
		"firecrawl markdown":  "# Meetings & Agendas\n\n| DATE & TIME | MEETING |\n| --- | --- |\n| NOVEMBER 10, 2026 – 9:30 AM | [COMMISSION MEETING](https://www.rctc.org/november2026agenda) |\n",
		"json":                `{"meetings":[]}`,
		"no meeting table":    `<html><body><table><tr><th>Name</th><th>Phone</th></tr><tr><td>COMMISSION MEETING</td><td>951</td></tr></table></body></html>`,
		"headers renamed":     `<table><tr><th>WHEN</th><th>EVENT</th></tr><tr><td>NOVEMBER 10, 2026 – 9:30 AM</td><td>COMMISSION MEETING</td></tr></table>`,
		"only empty rows":     string(page()),
		"no readable date":    string(page(listed{"TBD", "COMMISSION MEETING"}, listed{"SOON", "EXECUTIVE COMMITTEE"})),
		"no board meeting":    string(page(committee("NOVEMBER 16, 2026 &#8211; 10:00 AM"), listed{"NOVEMBER 10, 2026 &#8211; 9:30 AM", "BOARD MEETING"})),
		"tables without rows": `<table><thead><tr><th>DATE &amp; TIME</th><th>MEETING</th></tr></thead></table>`,
		"binary":              "\x00\x01\x02%PDF-1.7\xff\xfe",
	}
	for name, body := range cases {
		a, _ := logged(t, rctc.RCTC, fetchtest.Returning(body), rulesNow)
		got, err := a.Meetings(t.Context(), checkWindow(rulesNow))
		if !errors.Is(err, rctc.ErrMalformed) || sources.Retryable(err) || got != nil {
			t.Errorf("%s: meetings %v, err = %v; want permanent ErrMalformed", name, got, err)
		}
	}
}

func TestAPageWithBoardMeetingsOutsideTheWindowIsNotAnError(t *testing.T) {
	body := page(commission("MAY 13, 2026 &#8211; 9:30 AM", "may2026agenda"), committee("NOVEMBER 16, 2026 &#8211; 10:00 AM"))
	got, logs := meetings(t, body, rulesNow, checkWindow(rulesNow))
	if len(got) != 0 || len(warnings(logs)) != 0 {
		t.Errorf("meetings = %v, logs:\n%s", got, logs)
	}
}

func TestMalformedMarkupNeverPanics(t *testing.T) {
	bodies := []string{
		`<table><tr><th>DATE &amp; TIME<th>MEETING<tr><td>DECEMBER 9, 2026 9:30 AM<td><a href="/x">COMMISSION MEETING`,
		`<table><tr><th>DATE &amp; TIME</th><th>MEETING</th></tr><tr><td><table><tr><td>x</td></tr></table>DECEMBER 9, 2026</td><td>COMMISSION MEETING</td></tr></table>`,
		`</table></tr></td><a></a><table><tr><td>`,
		`<table><td><a href="x"><a href="y">COMMISSION MEETING</a></td></table>`,
		strings.Repeat(`<table><tr><th>DATE</th><th>MEETING</th>`, 2000),
		strings.Repeat("<tr><td>", 5000),
		`<table><tr><th>DATE &amp; TIME</th><th>MEETING</th></tr><tr><td>DECEMBER 99, 2026 99:99 PM</td><td>COMMISSION MEETING</td></tr><tr><td>DECEMBER 9, 2026</td><td>COMMISSION MEETING</td></tr></table>`,
	}
	for i, body := range bodies {
		a, _ := logged(t, rctc.RCTC, fetchtest.Returning(body), rulesNow)
		got, err := a.Meetings(t.Context(), checkWindow(rulesNow))
		if err == nil {
			if verr := sources.Validate(got); verr != nil {
				t.Errorf("body %d: invalid result: %v", i, verr)
			}
		} else if !errors.Is(err, rctc.ErrMalformed) {
			t.Errorf("body %d: err = %v", i, err)
		}
	}
}

func FuzzMeetings(f *testing.F) {
	f.Add(string(page(commission("DECEMBER 9, 2026 &#8211; 9:30 AM", "december2026agenda"))))
	f.Add(string(page(listed{"12/9/2026 NOON", "COMMISSION MEETING-CANCELED"})))
	f.Add(`<table><tr><th>DATE</th><th>MEETING</th></tr><tr><td>Feb 29, 2028 12 AM</td><td><a href="%zz">SPECIAL COMMISSION MEETING</a></td></tr></table>`)
	f.Fuzz(func(t *testing.T, body string) {
		a, err := rctc.New(rctc.RCTC, sources.Deps{Fetcher: fetchtest.Returning(body), Clock: sourcestest.NewClock(rulesNow)})
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Meetings(t.Context(), checkWindow(rulesNow))
		if err != nil {
			if !errors.Is(err, rctc.ErrMalformed) {
				t.Fatalf("err = %v", err)
			}
			return
		}
		if err := sources.Validate(got); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInvalidConfigs(t *testing.T) {
	cases := map[string]func(*rctc.Config){
		"no page url":       func(c *rctc.Config) { c.PageURL = "" },
		"relative page url": func(c *rctc.Config) { c.PageURL = "/meetings-and-agendas/" },
		"ftp page url":      func(c *rctc.Config) { c.PageURL = "ftp://www.rctc.org/" },
		"no boards":         func(c *rctc.Config) { c.Boards = nil },
		"blank board":       func(c *rctc.Config) { c.Boards = []rctc.Board{{Name: " "}} },
		"duplicate board": func(c *rctc.Config) {
			c.Boards = []rctc.Board{{Name: "Commission Meeting"}, {Name: "COMMISSION  MEETING"}}
		},
		"cancelled board":    func(c *rctc.Config) { c.Boards = []rctc.Board{{Name: "Commission Meeting - Cancelled"}} },
		"bad committee word": func(c *rctc.Config) { c.CommitteeWords = []string{"Executive Committee"} },
		"no time zone":       func(c *rctc.Config) { c.TimeZone = "" },
		"bad time zone":      func(c *rctc.Config) { c.TimeZone = "Mars/Olympus" },
		"bad start time":     func(c *rctc.Config) { c.StartTime = "9:30 AM" },
	}
	for name, mutate := range cases {
		cfg := rctc.RCTC
		cfg.Boards = append([]rctc.Board(nil), cfg.Boards...)
		mutate(&cfg)
		if _, err := rctc.New(cfg, sources.Deps{}); !errors.Is(err, rctc.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}

func TestConstructorPanicsOnAnInvalidConfig(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Constructor did not panic")
		}
	}()
	rctc.Constructor(rctc.Config{})
}

func TestConstructorBuildsTheProductionAdapter(t *testing.T) {
	fake := serving(fixture(t, "rctc_meetings_and_agendas.html"))
	a := rctc.Constructor(rctc.RCTC)(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(recordedAt)})
	got, err := a.Meetings(t.Context(), checkWindow(recordedAt))
	if err != nil || len(got) != 1 {
		t.Errorf("meetings = %v, err = %v", got, err)
	}
}
