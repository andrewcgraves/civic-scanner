package screen_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/screen"
)

func site(pages map[string]string) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		body, ok := pages[req.URL]
		if !ok {
			return fetch.Response{}, &fetch.Error{StatusCode: 404, Err: errors.New("not found: " + req.URL)}
		}
		return fetch.Response{Status: 200, ContentType: "text/html", Body: []byte(body), FinalURL: req.URL}, nil
	}}
}

func signals(r screen.Report) []string {
	var out []string
	for _, e := range r.Evidence {
		out = append(out, string(e.Supports)+":"+e.Signal+":"+e.URL)
	}
	return out
}

func urls(calls []fetch.Request) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.URL)
	}
	return out
}

func TestScreenFindsAgendasAndRecordingsAcrossPages(t *testing.T) {
	fake := site(map[string]string{
		"https://transit.example.org": `<html><body>
			<nav><a href="/about">About</a><a href="/board/meetings">Board Meetings</a></nav>
			<footer><a href="https://www.youtube.com/@transit" aria-label="YouTube"></a></footer>
		</body></html>`,
		"https://transit.example.org/board/meetings": `<html><body>
			<a href="/files/2026-11-agenda.pdf">November</a>
			<iframe src="https://www.youtube.com/embed/abc123"></iframe>
		</body></html>`,
	})
	report, err := screen.Screener{Fetcher: fake}.Screen(t.Context(), "https://transit.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != screen.VerdictBoth {
		t.Errorf("verdict = %s, want both; evidence %v", report.Verdict, signals(report))
	}
	want := []string{
		"agendas:agenda_pdf:https://transit.example.org/files/2026-11-agenda.pdf",
		"recordings:youtube:https://www.youtube.com/embed/abc123",
	}
	if got := signals(report); !slices.Equal(got, want) {
		t.Errorf("evidence = %v, want %v", got, want)
	}
	if got := report.Evidence[1].Page; got != "https://transit.example.org/board/meetings" {
		t.Errorf("evidence page = %q", got)
	}
	if got := urls(fake.Calls()); !slices.Equal(got, []string{"https://transit.example.org", "https://transit.example.org/board/meetings"}) {
		t.Errorf("fetched %v", got)
	}
	for _, c := range fake.Calls() {
		if !c.RawHTML || c.MaxBytes != screen.DefaultMaxBytes {
			t.Errorf("request %+v, want raw HTML with the default cap", c)
		}
	}
}

func TestScreenRecognisesMeetingPlatforms(t *testing.T) {
	tests := []struct {
		href string
		want string
	}{
		{"https://metro.legistar.com/Calendar.aspx", "agendas:legistar"},
		{"https://metro.legistar.com/Video.aspx?Mode=Granicus&ID1=1", "recordings:legistar_video"},
		{"https://metro.granicus.com/ViewPublisher.php?view_id=1", "agendas:granicus"},
		{"https://metro.granicus.com/MediaPlayer.php?view_id=1&clip_id=2", "recordings:granicus_video"},
		{"https://sbcta.primegov.com/public/portal", "agendas:primegov"},
		{"https://go.boarddocs.com/ca/agency/Board.nsf", "agendas:boarddocs"},
		{"https://agency.civicclerk.com/web/home.aspx", "agendas:civicclerk"},
		{"https://agency.swagit.com/play/1", "recordings:swagit"},
		{"https://transit.example.org/AgendaCenter", "agendas:agenda_center"},
		{"https://cdn.example.net/meetings/2026-11.mp4", "recordings:media_file"},
		{"https://vimeo.com/showcase/board-meetings", "recordings:vimeo"},
	}
	for _, tt := range tests {
		t.Run(tt.href, func(t *testing.T) {
			fake := site(map[string]string{"https://transit.example.org": `<a href="` + tt.href + `">link</a>`})
			report, err := screen.Screener{Fetcher: fake, MaxPages: 1}.Screen(t.Context(), "https://transit.example.org")
			if err != nil {
				t.Fatal(err)
			}
			got := signals(report)
			if len(got) != 1 || !strings.HasPrefix(got[0], tt.want+":") {
				t.Errorf("evidence = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestScreenIgnoresSocialVideoLinksOnTheHomePage(t *testing.T) {
	fake := site(map[string]string{"https://transit.example.org": `
		<a href="https://www.youtube.com/@transit"><img alt=""></a>
		<a href="https://vimeo.com/transit" title="Vimeo">Vimeo</a>
		<iframe src="https://www.youtube.com/embed/promo"></iframe>
		<a href="/news/annual-report.pdf">Annual report</a>`})
	report, err := screen.Screener{Fetcher: fake}.Screen(t.Context(), "https://transit.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != screen.VerdictNeither || len(report.Evidence) != 0 {
		t.Errorf("report = %+v, want neither with no evidence", report)
	}
}

func TestScreenCountsAVideoLinkNamedForMeetings(t *testing.T) {
	fake := site(map[string]string{"https://transit.example.org": `<a href="https://www.youtube.com/@transit">Watch board meetings</a>`})
	report, err := screen.Screener{Fetcher: fake}.Screen(t.Context(), "https://transit.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != screen.VerdictRecordingsOnly {
		t.Errorf("verdict = %s, want recordings_only", report.Verdict)
	}
}

func TestScreenVerdicts(t *testing.T) {
	tests := map[string]screen.Verdict{
		`<a href="https://metro.legistar.com/Calendar.aspx">Agendas</a>`:       screen.VerdictAgendasOnly,
		`<a href="https://agency.swagit.com/play/1">Video</a>`:                 screen.VerdictRecordingsOnly,
		`<a href="/contact">Contact</a>`:                                       screen.VerdictNeither,
		`<a href="https://agency.swagit.com/">v</a><a href="a.pdf">Agenda</a>`: screen.VerdictBoth,
	}
	for body, want := range tests {
		fake := site(map[string]string{"https://transit.example.org": body})
		report, err := screen.Screener{Fetcher: fake, MaxPages: 1}.Screen(t.Context(), "https://transit.example.org")
		if err != nil {
			t.Fatal(err)
		}
		if report.Verdict != want {
			t.Errorf("%s: verdict = %s, want %s", body, report.Verdict, want)
		}
	}
}

func TestScreenFollowsPlatformsFirstAndStopsAtTheBudget(t *testing.T) {
	var links strings.Builder
	for _, path := range []string{"/meetings", "/board", "/agendas", "/committee", "/news", "/minutes", "/council"} {
		links.WriteString(`<a href="` + path + `">` + strings.TrimPrefix(path, "/") + `</a>`)
	}
	links.WriteString(`<a href="https://metro.granicus.com/ViewPublisher.php?view_id=1">Archive</a>`)
	links.WriteString(`<a href="https://elsewhere.example.com/meetings">Partner meetings</a>`)
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		body := ""
		if req.URL == "https://transit.example.org" {
			body = links.String()
		}
		return fetch.Response{Status: 200, Body: []byte(body), FinalURL: req.URL}, nil
	}}
	report, err := screen.Screener{Fetcher: fake, MaxPages: 4}.Screen(t.Context(), "https://transit.example.org")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://transit.example.org",
		"https://metro.granicus.com/ViewPublisher.php?view_id=1",
		"https://transit.example.org/meetings",
		"https://transit.example.org/agendas",
	}
	if got := urls(fake.Calls()); !slices.Equal(got, want) {
		t.Errorf("fetched %v, want %v", got, want)
	}
	if len(report.Pages) != 4 {
		t.Errorf("pages = %+v", report.Pages)
	}
}

func TestScreenFollowsTheSiteAfterARedirect(t *testing.T) {
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		switch req.URL {
		case "https://transit.example.org":
			return fetch.Response{Status: 200, Body: []byte(`<a href="/meetings">Meetings</a><a href="https://other.example.com/meetings">Other</a>`), FinalURL: "https://www.transit-agency.example.com/home"}, nil
		case "https://www.transit-agency.example.com/meetings":
			return fetch.Response{Status: 200, Body: []byte(`<a href="agenda.pdf">Agenda</a>`), FinalURL: req.URL}, nil
		}
		return fetch.Response{}, &fetch.Error{StatusCode: 404, Err: errors.New("unexpected " + req.URL)}
	}}
	report, err := screen.Screener{Fetcher: fake}.Screen(t.Context(), "https://transit.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if got := signals(report); !slices.Equal(got, []string{"agendas:agenda_pdf:https://www.transit-agency.example.com/agenda.pdf"}) {
		t.Errorf("evidence = %v", got)
	}
	if n := len(fake.Calls()); n != 2 {
		t.Errorf("fetched %v", urls(fake.Calls()))
	}
}

func TestScreenRecordsAPermanentPageFailureAndCarriesOn(t *testing.T) {
	fake := site(map[string]string{
		"https://transit.example.org":         `<a href="/meetings">Meetings</a><a href="/agendas">Agendas</a>`,
		"https://transit.example.org/agendas": `<a href="https://metro.legistar.com/">Legistar</a>`,
	})
	report, err := screen.Screener{Fetcher: fake, MaxPages: 2}.Screen(t.Context(), "https://transit.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != screen.VerdictAgendasOnly {
		t.Errorf("verdict = %s", report.Verdict)
	}
	if len(report.Pages) != 3 || report.Pages[1].URL != "https://transit.example.org/meetings" || report.Pages[1].Error == "" {
		t.Errorf("pages = %+v, want the failed page recorded with its error", report.Pages)
	}
}

func TestScreenFailsWhenTheHomePageFails(t *testing.T) {
	permanent := &fetch.Error{StatusCode: 404, Err: errors.New("gone")}
	_, err := screen.Screener{Fetcher: fetchtest.Failing(permanent)}.Screen(t.Context(), "https://transit.example.org")
	if !errors.Is(err, permanent) || screen.Retryable(err) {
		t.Errorf("err = %v, want the permanent fetch error", err)
	}
	refused := &fetch.Error{Err: fetch.ErrForbiddenAddress}
	_, err = screen.Screener{Fetcher: fetchtest.Failing(refused)}.Screen(t.Context(), "https://transit.example.org")
	if !errors.Is(err, fetch.ErrForbiddenAddress) || screen.Retryable(err) {
		t.Errorf("err = %v, want a permanent refusal", err)
	}
}

func TestScreenFailsOnARetryablePageError(t *testing.T) {
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if req.URL == "https://transit.example.org" {
			return fetch.Response{Status: 200, Body: []byte(`<a href="/meetings">Meetings</a>`), FinalURL: req.URL}, nil
		}
		return fetch.Response{}, &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}
	}}
	_, err := screen.Screener{Fetcher: fake}.Screen(t.Context(), "https://transit.example.org")
	if !screen.Retryable(err) || !strings.Contains(err.Error(), "/meetings") {
		t.Errorf("err = %v, want a retryable error naming the page", err)
	}
}

func TestScreenWithoutFetcher(t *testing.T) {
	_, err := screen.Screener{}.Screen(t.Context(), "https://transit.example.org")
	if !errors.Is(err, screen.ErrNoFetcher) || screen.Retryable(err) {
		t.Errorf("err = %v", err)
	}
}

func TestScreenCapsEvidencePerKind(t *testing.T) {
	var body strings.Builder
	for i := range 30 {
		body.WriteString(`<a href="/agendas/` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `.pdf">Agenda</a>`)
	}
	fake := site(map[string]string{"https://transit.example.org": body.String()})
	report, err := screen.Screener{Fetcher: fake, MaxPages: 1}.Screen(t.Context(), "https://transit.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Evidence) != screen.MaxEvidencePerKind {
		t.Errorf("evidence = %d, want %d", len(report.Evidence), screen.MaxEvidencePerKind)
	}
}

func TestRetryable(t *testing.T) {
	if !screen.Retryable(context.DeadlineExceeded) || !screen.Retryable(&fetch.Error{Retryable: true, Err: errors.New("x")}) {
		t.Error("deadline and retryable fetch errors must be retryable")
	}
	if screen.Retryable(fetch.ErrNotConfigured) {
		t.Error("not configured must be permanent")
	}
}
