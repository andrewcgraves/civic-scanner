package primegov_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/primegov"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

func TestFetchErrorsKeepTheirRetryability(t *testing.T) {
	cases := []struct {
		name      string
		err       *fetch.Error
		retryable bool
	}{
		{"server error", &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}, true},
		{"not found", &fetch.Error{StatusCode: 404, Err: errors.New("not found")}, false},
		{"too large", &fetch.Error{StatusCode: 200, Err: fetch.ErrTooLarge}, false},
	}
	for _, c := range cases {
		_, err := adapter(t, primegov.SBCTA, fetchtest.Failing(c.err), rulesNow).Meetings(t.Context(), checkWindow(rulesNow))
		if !errors.Is(err, c.err) || sources.Retryable(err) != c.retryable {
			t.Errorf("%s: err = %v, retryable %v; want %v wrapped, retryable %v", c.name, err, sources.Retryable(err), c.err, c.retryable)
		}
	}
}

func TestArchiveFetchErrorFailsTheCheck(t *testing.T) {
	failure := &fetch.Error{StatusCode: 500, Retryable: true, Err: errors.New("boom")}
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if strings.HasPrefix(req.URL, archivedURL) {
			return fetch.Response{}, failure
		}
		return fetch.Response{Status: 200, Body: list(t, boardMeeting(1, "2026-12-02T10:00:00")), FinalURL: req.URL}, nil
	}}
	_, err := adapter(t, primegov.SBCTA, fake, rulesNow).Meetings(t.Context(), checkWindow(rulesNow))
	if !errors.Is(err, failure) || !sources.Retryable(err) {
		t.Errorf("err = %v, want the retryable archive error", err)
	}
}

func TestCanceledContextIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := adapter(t, primegov.SBCTA, serving(list(t), nil), rulesNow).Meetings(ctx, checkWindow(rulesNow))
	if !sources.Retryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}

func TestMalformedResponsesArePermanent(t *testing.T) {
	committee := boardMeeting(5, "2026-12-09T09:00:00").titled("General Policy Committee").in(4)
	cases := []struct {
		name     string
		upcoming []byte
		archived []byte
		want     string
	}{
		{"firecrawl markdown", []byte("# Upcoming Meetings\n\n| Board of Directors | Dec 02, 2026 |"), nil, "invalid character '#'"},
		{"an object", []byte(`{"message":"An error has occurred."}`), nil, "cannot unmarshal object"},
		{"html error page", []byte("<!DOCTYPE html><title>Document Not Found</title>"), nil, "invalid character '<'"},
		{"nothing decodes", []byte(`[1, "x", {"id": "100"}]`), nil, "none of the 3 meetings decodes"},
		{"fields renamed", []byte(`[{"meetingId": 100, "committee": 1, "name": "Board of Directors", "start": "2026-12-02T10:00:00"}]`), nil, "none of the 1 meetings has an id, a committee id, a title and a date"},
		{"committee ids changed", list(t, committee, boardMeeting(1, "2026-12-02T10:00:00").in(41)), nil, "none of the 2 meetings belongs to committee [1]"},
		{"every board date unreadable", list(t, func() mtg { m := boardMeeting(1, "2026-12-02T10:00:00"); m.DateTime, m.Date = "soon", "TBD"; return m }(), committee), nil, "all 1 board meetings in the window are unreadable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			archived := map[int][]byte{}
			if c.archived != nil {
				archived[rulesNow.Year()] = c.archived
			}
			got, err := adapter(t, primegov.SBCTA, serving(c.upcoming, archived), rulesNow).Meetings(t.Context(), checkWindow(rulesNow))
			if !errors.Is(err, primegov.ErrMalformed) || sources.Retryable(err) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want permanent ErrMalformed containing %q", err, c.want)
			}
			if got != nil {
				t.Errorf("meetings = %+v, want none with an error", got)
			}
		})
	}
}

func TestNullListIsNoMeetings(t *testing.T) {
	fake := serving(list(t, boardMeeting(1, "2026-12-02T10:00:00")), map[int][]byte{rulesNow.Year(): []byte("null")})
	got, err := adapter(t, primegov.SBCTA, fake, rulesNow).Meetings(t.Context(), checkWindow(rulesNow))
	if err != nil || len(got) != 1 {
		t.Errorf("a null archive: %+v, %v; want the upcoming meeting", got, err)
	}
}

func TestUnreadableItemsAreSkippedAndTheRestKept(t *testing.T) {
	good := boardMeeting(1, "2026-12-02T10:00:00", packet(645, "2026-10-29T21:08:37.74"))
	body := []byte(`[` +
		`"not an object",` +
		`{"id": 7, "committeeId": 1, "title": "Board of Directors", "dateTime": "2026-12-16T10:00:00", "documentList": [{"templateId": "x"}, {"templateId": 9, "templateName": "Packet", "compileOutputType": 1}]},` +
		`{"id": 0, "committeeId": 1, "title": "Board of Directors", "dateTime": "2026-12-09T10:00:00"},` +
		`{"id": 8, "committeeId": 1, "title": "Board of Directors", "dateTime": "someday", "date": ""},` +
		`{"id": 9, "committeeId": 1, "title": "Board of Directors", "dateTime": "2026-12-30T10:00:00", "documentList": null, "videoUrl": null, "time": null, "date": null},` +
		string(list(t, good))[1:])
	a, logs := logged(t, primegov.SBCTA, serving(body, nil), rulesNow)
	got, err := a.Meetings(t.Context(), checkWindow(rulesNow))
	if err != nil {
		t.Fatal(err)
	}
	if err := sources.Validate(got); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, m := range got {
		ids = append(ids, m.SourceID)
	}
	if strings.Join(ids, ",") != "1,7,9" {
		t.Errorf("ids %v, want 1,7,9", ids)
	}
	if got[1].AgendaURL != packetURL+"9&compileOutputType=1" {
		t.Errorf("meeting 7 agenda %q, want the readable packet", got[1].AgendaURL)
	}
	for _, want := range []string{
		"primegov: skipped a meeting that does not decode",
		"primegov: skipped a document that does not decode",
		"primegov: skipped a meeting without an id, committee, title or date",
		"primegov: skipped a board meeting without a readable date\" host=sbcta.primegov.com meeting=8",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}

func TestTruncatedAndCorruptedResponsesNeverPanic(t *testing.T) {
	bodies := [][]byte{fixture(t, "sbcta_upcoming.json"), fixture(t, "sbcta_archived_2026.json")}
	for _, body := range bodies {
		for cut := 0; cut < len(body); cut += 97 {
			for _, b := range [][]byte{body[:cut], corrupt(body, cut)} {
				a := adapter(t, primegov.SBCTA, serving(b, map[int][]byte{2026: b}), recordedAt)
				got, err := a.Meetings(t.Context(), wide)
				if err == nil {
					if verr := sources.Validate(got); verr != nil {
						t.Fatalf("cut %d: invalid result: %v", cut, verr)
					}
					for _, m := range got {
						if m.AgendaURL != "" && !strings.HasPrefix(m.AgendaURL, packetURL) {
							t.Fatalf("cut %d: agenda %q is off the PrimeGov host", cut, m.AgendaURL)
						}
					}
				} else if !errors.Is(err, primegov.ErrMalformed) {
					t.Fatalf("cut %d: err = %v, want ErrMalformed", cut, err)
				}
			}
		}
	}
}

func corrupt(body []byte, at int) []byte {
	out := append([]byte{}, body...)
	out[at] = '"'
	return out
}

func TestInvalidConfigs(t *testing.T) {
	cases := map[string]func(*primegov.Config){
		"host with path":        func(c *primegov.Config) { c.Host = "sbcta.primegov.com/api" },
		"host off primegov":     func(c *primegov.Config) { c.Host = "sbcta.example.com" },
		"no agenda host":        func(c *primegov.Config) { c.AgendaHost = "" },
		"agenda host elsewhere": func(c *primegov.Config) { c.AgendaHost = "pgwest.blob.core.windows.net" },
		"agenda host lookalike": func(c *primegov.Config) { c.AgendaHost = "sbcta.primegov.com.evil.example" },
		"agenda host suffix":    func(c *primegov.Config) { c.AgendaHost = "evilprimegov.com" },
		"agenda host bare":      func(c *primegov.Config) { c.AgendaHost = "primegov.com" },
		"agenda host scheme":    func(c *primegov.Config) { c.AgendaHost = "https://sbcta.primegov.com" },
		"agenda host port":      func(c *primegov.Config) { c.AgendaHost = "sbcta.primegov.com:8443" },
		"agenda host case":      func(c *primegov.Config) { c.AgendaHost = "SBCTA.primegov.com" },
		"agenda host userinfo":  func(c *primegov.Config) { c.AgendaHost = "user@sbcta.primegov.com" },
		"no host":               func(c *primegov.Config) { c.Host = "" },
		"no committee":          func(c *primegov.Config) { c.CommitteeIDs = nil },
		"zero committee":        func(c *primegov.Config) { c.CommitteeIDs = []int{0} },
		"no board name":         func(c *primegov.Config) { c.BoardName = " " },
		"special board name":    func(c *primegov.Config) { c.BoardName = "Special Board of Directors" },
		"no agenda template":    func(c *primegov.Config) { c.AgendaTemplates = nil },
		"blank agenda template": func(c *primegov.Config) { c.AgendaTemplates = []string{""} },
		"blank evidence":        func(c *primegov.Config) { c.EvidenceTemplates = []string{" "} },
		"relative page url":     func(c *primegov.Config) { c.PageURL = "/board/" },
		"no time zone":          func(c *primegov.Config) { c.TimeZone = "" },
		"bad time zone":         func(c *primegov.Config) { c.TimeZone = "Pacific/Nowhere" },
		"bad start time":        func(c *primegov.Config) { c.StartTime = "10am" },
	}
	deps := sources.Deps{Fetcher: fetchtest.Returning("[]"), Clock: sourcestest.NewClock(rulesNow)}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := primegov.SBCTA
			cfg.CommitteeIDs = append([]int{}, cfg.CommitteeIDs...)
			mutate(&cfg)
			if _, err := primegov.New(cfg, deps); !errors.Is(err, primegov.ErrInvalidConfig) {
				t.Errorf("New: err = %v, want ErrInvalidConfig", err)
			}
			defer func() {
				if recover() == nil {
					t.Error("Constructor did not panic")
				}
			}()
			primegov.Constructor(cfg)
		})
	}
	if primegov.Constructor(primegov.SBCTA)(deps) == nil {
		t.Error("Constructor(SBCTA) returned nil")
	}
}

func TestAgendaURLIsAlwaysHTTPSOnTheAgendaHost(t *testing.T) {
	body := []byte(`[{"id": 1, "committeeId": 1, "title": "Board of Directors", "dateTime": "2026-12-02T10:00:00", "documentList": [` +
		`{"templateId": 645, "templateName": "Packet", "compileOutputType": 1, "publishDate": "2026-11-25T10:00:00", "link": "https://evil.example/agenda.pdf"},` +
		`{"templateId": "646@evil.example", "templateName": "Packet", "compileOutputType": 1, "publishDate": "2026-11-26T10:00:00"},` +
		`{"templateId": 647.5, "templateName": "Packet", "compileOutputType": 1, "publishDate": "2026-11-27T10:00:00"},` +
		`{"templateId": -648, "templateName": "Packet", "compileOutputType": -1, "publishDate": "2026-11-28T10:00:00"},` +
		`{"templateId": 649, "templateName": "Packet\u0000//evil.example", "compileOutputType": 1, "publishDate": "2026-11-29T10:00:00"}` +
		`]}]`)
	got, err := adapter(t, primegov.SBCTA, serving(body, nil), rulesNow).Meetings(t.Context(), checkWindow(rulesNow))
	if err != nil {
		t.Fatal(err)
	}
	if m := one(t, got); m.AgendaURL != packetURL+"645&compileOutputType=1" {
		t.Errorf("agenda %q, want the packet on sbcta.primegov.com and nothing from the tampered documents", m.AgendaURL)
	}

	cfg := primegov.SBCTA
	cfg.AgendaHost = "files.primegov.com"
	got, err = adapter(t, cfg, serving(body, nil), rulesNow).Meetings(t.Context(), checkWindow(rulesNow))
	if err != nil {
		t.Fatal(err)
	}
	if m := one(t, got); m.AgendaURL != "https://files.primegov.com/Public/CompiledDocument?meetingTemplateId=645&compileOutputType=1" {
		t.Errorf("agenda %q, want it on the configured agenda host", m.AgendaURL)
	}
}
