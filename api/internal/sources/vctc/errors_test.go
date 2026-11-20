package vctc_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/vctc"
)

var errorsNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

const novAgenda = uploads + "2026/10/AGENDA-COMMISSION-11.6.26.pdf"

func TestFetchErrorsKeepTheirRetryability(t *testing.T) {
	cases := []struct {
		name      string
		err       *fetch.Error
		retryable bool
	}{
		{"server error", &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}, true},
		{"not found", &fetch.Error{StatusCode: 404, Err: errors.New("not found")}, false},
		{"forbidden", &fetch.Error{StatusCode: 403, Err: errors.New("forbidden")}, false},
	}
	for _, c := range cases {
		_, err := adapter(t, agendasOnly(), fetchtest.Failing(c.err), errorsNow).Meetings(t.Context(), checkWindow(errorsNow))
		if !errors.Is(err, c.err) || sources.Retryable(err) != c.retryable {
			t.Errorf("%s: err = %v, retryable %v; want %v wrapped, retryable %v", c.name, err, sources.Retryable(err), c.err, c.retryable)
		}
	}
}

func TestYouTubeFetchErrorsKeepTheirRetryability(t *testing.T) {
	failure := &fetch.Error{StatusCode: 500, Retryable: true, Err: errors.New("boom")}
	body := posts(t, commission(1, "November 2026", "2026-11-06", "09:00", novAgenda))
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if strings.HasPrefix(req.URL, "https://www.youtube.com/") {
			return fetch.Response{}, failure
		}
		return fetch.Response{Status: 200, Body: body, FinalURL: req.URL}, nil
	}}
	_, err := adapter(t, withYouTube(), fake, errorsNow).Meetings(t.Context(), checkWindow(errorsNow))
	if !errors.Is(err, failure) || !sources.Retryable(err) || !strings.Contains(err.Error(), "youtube feed") {
		t.Errorf("err = %v, want the retryable feed error", err)
	}
}

func TestCanceledContextIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := adapter(t, agendasOnly(), serving(posts(t), nil), errorsNow).Meetings(ctx, checkWindow(errorsNow))
	if !sources.Retryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}

func TestMalformedResponsesArePermanent(t *testing.T) {
	good := posts(t, commission(1, "November 2026", "2026-11-06", "09:00", novAgenda))
	cases := []struct {
		name  string
		posts []byte
		feed  []byte
		want  string
	}{
		{"firecrawl markdown", []byte("# Commission Meeting – November 2026\n\n[Agenda](https://www.goventura.org/a.pdf)"), nil, "meetings: invalid character"},
		{"an object", []byte(`{"code":"rest_no_route"}`), nil, "cannot unmarshal object"},
		{"nothing decodes", []byte(`[1, "x", {"id": "24950"}]`), nil, "none of the 3 posts decodes"},
		{"meeting date field renamed", posts(t, renamedMeta(commission(1, "November 2026", "2026-11-06", "09:00", novAgenda)), renamedMeta(commission(2, "December 2026", "2026-12-04", "09:00", ""))), nil, "none of the 2 posts is a board meeting post with an id, a link and a meta._meeting_date field"},
		{"meta left out", posts(t, withoutMeta(commission(1, "November 2026", "2026-11-06", "09:00", novAgenda)), withoutMeta(commission(2, "December 2026", "2026-12-04", "09:00", ""))), nil, "none of the 2 posts is a board meeting post"},
		{"titles renamed", posts(t, commission(1, "November 2026", "2026-11-06", "09:00", novAgenda).titled("Commission – November 2026"), commission(2, "December 2026", "2026-12-04", "09:00", "").titled("Commission – December 2026")), nil, "none of the 2 posts is a board meeting post"},
		{"ids gone", posts(t, commission(0, "November 2026", "2026-11-06", "09:00", novAgenda)), nil, "none of the 1 posts is a board meeting post"},
		{"feed not xml", good, []byte("# VCTC videos"), "youtube feed: EOF"},
		{"feed not atom", good, []byte(`<rss><channel></channel></rss>`), "youtube feed: expected element type"},
		{"every video unreadable", good, feed(videoEntry{"", "VCTC Commission Meeting - November 6, 2026", "2026-11-07T06:00:00Z"}, videoEntry{"aaaaaaaaaaa", "VCTC Commission Meeting", "yesterday"}), "all 2 meeting videos in the window are unreadable"},
	}
	for _, c := range cases {
		_, err := adapter(t, withYouTube(), serving(c.posts, c.feed), errorsNow).Meetings(t.Context(), checkWindow(errorsNow))
		if !errors.Is(err, vctc.ErrMalformed) || sources.Retryable(err) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want permanent ErrMalformed containing %q", c.name, err, c.want)
		}
	}
}

func renamedMeta(b built) built {
	b.Meta = map[string]any{"meeting_date": "2026-11-06", "meeting_time": "09:00"}
	return b
}

func withoutMeta(b built) built {
	b.Meta = nil
	return b
}

func TestOneUnreadablePostDoesNotCancelTheCheck(t *testing.T) {
	cases := []struct {
		name string
		bad  any
	}{
		{"undecodable", map[string]any{"id": "x", "title": map[string]string{"rendered": "Commission Meeting – December 2026"}}},
		{"no meeting date", commission(2, "December 2026", "", "09:00", "")},
		{"meta as an empty list", func() built { b := commission(2, "December 2026", "", "", ""); b.Meta = []any{}; return b }()},
		{"meta of the wrong type", func() built {
			b := commission(2, "December 2026", "", "", "")
			b.Meta = map[string]any{"_meeting_date": 20261204}
			return b
		}()},
		{"no id", commission(0, "December 2026", "2026-12-04", "09:00", "")},
		{"relative link", func() built {
			b := commission(2, "December 2026", "2026-12-04", "09:00", "")
			b.Link = "/meeting/x/"
			return b
		}()},
	}
	for _, c := range cases {
		body := posts(t, commission(1, "November 2026", "2026-11-06", "09:00", novAgenda), c.bad)
		a, logs := logged(t, agendasOnly(), serving(body, nil), errorsNow)
		got, err := a.Meetings(t.Context(), checkWindow(errorsNow))
		if err != nil || len(got) != 1 || got[0].SourceID != "1" {
			t.Errorf("%s: got %+v, %v; want meeting 1 alone", c.name, got, err)
		}
		if !strings.Contains(logs.String(), "level=WARN") {
			t.Errorf("%s: no warning:\n%s", c.name, logs)
		}
	}
}

func TestUnreadablePostsOutsideTheWindowAreQuiet(t *testing.T) {
	body := posts(t,
		commission(1, "November 2026", "2026-11-06", "09:00", novAgenda),
		commission(2, "March 2011", "", "", ""),
		commission(3, "July 2011", "2011-07-xx", "", ""),
	)
	a, logs := logged(t, agendasOnly(), serving(body, nil), errorsNow)
	got, err := a.Meetings(t.Context(), checkWindow(errorsNow))
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), `level=DEBUG msg="vctc: skipped a board meeting post outside the window" host=www.goventura.org post=2`) {
		t.Errorf("logs:\n%s", logs)
	}
}

func TestUndatableTitleIsNotInTheWindow(t *testing.T) {
	body := posts(t, commission(1, "", "", "", "").titled("Commission Meeting"), commission(2, "", "", "", "").titled("Commission Meeting – Fall 2026"))
	a, logs := logged(t, agendasOnly(), serving(body, nil), errorsNow)
	got, err := a.Meetings(t.Context(), checkWindow(errorsNow))
	if err != nil || len(got) != 0 || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("got %+v, %v; want none, no error and no warning:\n%s", got, err, logs)
	}
}

func TestEmptyListHasNoMeetings(t *testing.T) {
	got, err := adapter(t, withYouTube(), serving(posts(t), feed()), errorsNow).Meetings(t.Context(), checkWindow(errorsNow))
	if err != nil || len(got) != 0 {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestInvalidConfigs(t *testing.T) {
	mutate := func(f func(*vctc.Config)) vctc.Config {
		cfg := agendasOnly()
		cfg.Boards = append([]vctc.Board(nil), cfg.Boards...)
		f(&cfg)
		return cfg
	}
	cases := map[string]vctc.Config{
		"empty host":       mutate(func(c *vctc.Config) { c.Host = "" }),
		"host with a path": mutate(func(c *vctc.Config) { c.Host = "www.goventura.org/x" }),
		"no boards":        mutate(func(c *vctc.Config) { c.Boards = nil }),
		"zero type":        mutate(func(c *vctc.Config) { c.Boards[0].TypeID = 0 }),
		"blank name":       mutate(func(c *vctc.Config) { c.Boards[1].Name = "  " }),
		"page size":        mutate(func(c *vctc.Config) { c.PageSize = 101 }),
		"committee phrase": mutate(func(c *vctc.Config) { c.CommitteeWords = []string{"Admin Finance"} }),
		"empty committee":  mutate(func(c *vctc.Config) { c.CommitteeWords = []string{""} }),
		"no zone":          mutate(func(c *vctc.Config) { c.TimeZone = "" }),
		"bad zone":         mutate(func(c *vctc.Config) { c.TimeZone = "Mars/Olympus" }),
		"bad start":        mutate(func(c *vctc.Config) { c.StartTime = "9am" }),
		"bad channel": mutate(func(c *vctc.Config) {
			c.YouTube = vctc.YouTube{ChannelID: "VCTC001", TitlePrefix: "x", MaxDelay: time.Hour}
		}),
		"no prefix": mutate(func(c *vctc.Config) {
			c.YouTube = vctc.YouTube{ChannelID: vctc.VCTCYouTube.ChannelID, MaxDelay: time.Hour}
		}),
		"no delay": mutate(func(c *vctc.Config) {
			c.YouTube = vctc.YouTube{ChannelID: vctc.VCTCYouTube.ChannelID, TitlePrefix: "x"}
		}),
		"channel characters": mutate(func(c *vctc.Config) {
			c.YouTube = vctc.YouTube{ChannelID: "UC" + strings.Repeat("!", 22), TitlePrefix: "x", MaxDelay: time.Hour}
		}),
	}
	for name, cfg := range cases {
		if _, err := vctc.New(cfg, sources.Deps{}); !errors.Is(err, vctc.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("Constructor did not panic on an invalid config")
		}
	}()
	vctc.Constructor(cases["no boards"])
}

func TestConstructorUsesTheDepsOfEachRegistry(t *testing.T) {
	construct := vctc.Constructor(agendasOnly())
	fake := serving(posts(t), nil)
	if _, err := construct(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(errorsNow)}).Meetings(t.Context(), checkWindow(errorsNow)); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls()) != 1 {
		t.Errorf("calls = %v", fake.Calls())
	}
}
