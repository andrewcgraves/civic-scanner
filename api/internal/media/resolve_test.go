package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
)

const (
	playerURL  = "https://metrolink.granicus.com/MediaPlayer.php?view_id=8&clip_id=1438"
	streamURL  = "https://archive-stream.granicus.com/OnDemand/_definst_/mp4:archive/metrolink/metrolink_7e11fb50-dfa7-11f1-9c50-005056a84798.mp4/playlist.m3u8"
	videoURL   = "https://archive-video.granicus.com/metrolink/metrolink_7e11fb50-dfa7-11f1-9c50-005056a84798.mp4"
	youtubeURL = "https://www.youtube.com/watch?v=abc123"
)

func page(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func servingPage(body, finalURL string) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if finalURL == "" {
			finalURL = req.URL
		}
		return fetch.Response{Status: 200, ContentType: "text/html; charset=UTF-8", Body: []byte(body), FinalURL: finalURL}, nil
	}}
}

func resolver(fetcher fetch.Fetcher) Resolver {
	return Resolver{Fetcher: fetcher, Hosts: DefaultHosts}
}

func TestResolveReadsTheStreamFromARecordedGranicusPlayerPage(t *testing.T) {
	fake := servingPage(page(t, "metrolink_player_1438.html"), "https://metrolink.granicus.com/player/clip/1438?view_id=8&redirect=true")
	src, err := resolver(fake).Resolve(t.Context(), playerURL)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != (Source{URL: streamURL, Via: ViaPlayer}) {
		t.Errorf("Resolve = %+v", src)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].URL != playerURL || calls[0].MaxBytes != maxPlayerPageBytes {
		t.Errorf("fetch calls = %+v", calls)
	}
}

func TestResolvePlayerPageVariants(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"escaped only":        {`var standardVideoUrl = "https:\/\/archive-stream.granicus.com\/x\/clip.mp4\/playlist.m3u8";`, "https://archive-stream.granicus.com/x/clip.mp4/playlist.m3u8"},
		"file only":           {`<a href="https://archive-video.granicus.com/metrolink/clip.mp4">Download</a>`, "https://archive-video.granicus.com/metrolink/clip.mp4"},
		"stream over file":    {`"https://archive-video.granicus.com/a.mp4" "https://archive-stream.granicus.com/b/playlist.m3u8"`, "https://archive-stream.granicus.com/b/playlist.m3u8"},
		"first file wins":     {`"https://archive-video.granicus.com/a.mp4" "https://archive-video.granicus.com/b.mp4"`, "https://archive-video.granicus.com/a.mp4"},
		"other hosts ignored": {`"https://evil.test/a.m3u8" "https://archive-video.granicus.com/a.mp4"`, "https://archive-video.granicus.com/a.mp4"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src, err := resolver(servingPage(tc.body, "")).Resolve(t.Context(), playerURL)
			if err != nil || src.URL != tc.want {
				t.Errorf("Resolve = %+v, %v; want %s", src, err, tc.want)
			}
		})
	}
}

func TestResolvePlayerPageFailures(t *testing.T) {
	cases := map[string]struct {
		fetcher fetch.Fetcher
		want    error
	}{
		"no media": {servingPage("<html><body>This clip has no video.</body></html>", ""), ErrNoMedia},
		"only a stream on another host": {
			servingPage(`video_url="https://cdn.example.test/clip/playlist.m3u8"`, ""), ErrNoMedia,
		},
		"bot check": {servingPage("<html><title>Pardon Our Interruption</title></html>", ""), ErrBlocked},
		"redirected off granicus": {
			servingPage(page(t, "metrolink_player_1438.html"), "https://www.example.test/error"), ErrNoMedia,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := resolver(tc.fetcher).Resolve(t.Context(), playerURL)
			if !errors.Is(err, tc.want) {
				t.Errorf("Resolve = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestResolveKeepsFetchErrorsClassified(t *testing.T) {
	retry := &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}
	_, err := resolver(fetchtest.Failing(retry)).Resolve(t.Context(), playerURL)
	if !errors.Is(err, retry) || !IsRetryable(err) {
		t.Errorf("Resolve = %v, want the retryable fetch error", err)
	}
	gone := &fetch.Error{StatusCode: 404, Err: errors.New("not found")}
	_, err = resolver(fetchtest.Failing(gone)).Resolve(t.Context(), playerURL)
	if !errors.Is(err, gone) || IsRetryable(err) {
		t.Errorf("Resolve = %v, want the permanent fetch error", err)
	}
}

func TestResolveWithoutFetcherIsUnavailable(t *testing.T) {
	if _, err := resolver(nil).Resolve(t.Context(), playerURL); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Resolve = %v, want ErrUnavailable", err)
	}
}

func TestResolveDirectMediaOnlyOnMediaHosts(t *testing.T) {
	fake := fetchtest.Failing(errors.New("must not fetch"))
	src, err := resolver(fake).Resolve(t.Context(), videoURL)
	if err != nil || src != (Source{URL: videoURL, Via: ViaDirect}) {
		t.Errorf("Resolve = %+v, %v", src, err)
	}
	castus := "https://dlttx48mxf9m3.cloudfront.net/outputs/6af62b4f05fc227eedc662d8/Default/HLS/out.m3u8"
	if src, err := resolver(fake).Resolve(t.Context(), castus); err != nil || src != (Source{URL: castus, Via: ViaDirect}) {
		t.Errorf("Resolve(castus) = %+v, %v", src, err)
	}
	cases := map[string]error{
		"https://media.example.test/meeting.mp4":                  ErrUnsupported,
		"https://d1234abcd.cloudfront.net/outputs/x/out.m3u8":     ErrUnsupported,
		"https://cloud.castus.tv/vod/rivcotv/video/6af62b4f05fc":  ErrUnsupported,
		"https://archive-video.granicus.com/metrolink/index.html": ErrUnsupported,
		"ftp://archive-video.granicus.com/a.mp4":                  ErrInvalidURL,
		"file:///etc/passwd":                                      ErrInvalidURL,
		"https:///a.mp4":                                          ErrInvalidURL,
		"::bad":                                                   ErrInvalidURL,
	}
	for u, want := range cases {
		if _, err := resolver(fake).Resolve(t.Context(), u); !errors.Is(err, want) {
			t.Errorf("Resolve(%q) = %v, want %v", u, err, want)
		}
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("fetched %d times", len(calls))
	}
}

func TestResolveYouTube(t *testing.T) {
	var got string
	r := resolver(nil)
	r.YouTube = func(_ context.Context, pageURL string) (string, error) {
		got = pageURL
		return "https://rr3---sn-a5mekn6s.googlevideo.com/videoplayback?itag=251", nil
	}
	src, err := r.Resolve(t.Context(), youtubeURL)
	if err != nil || src.Via != ViaYouTube || !strings.Contains(src.URL, "googlevideo.com") || got != youtubeURL {
		t.Errorf("Resolve = %+v, %v (asked %q)", src, err, got)
	}

	r.YouTube = func(context.Context, string) (string, error) { return "https://evil.test/a.webm", nil }
	if _, err := r.Resolve(t.Context(), youtubeURL); !errors.Is(err, ErrNoMedia) {
		t.Errorf("Resolve with a foreign media host = %v, want ErrNoMedia", err)
	}
	r.YouTube = func(context.Context, string) (string, error) { return "", ErrNetwork }
	if _, err := r.Resolve(t.Context(), "https://youtu.be/abc123"); !errors.Is(err, ErrNetwork) {
		t.Errorf("Resolve with a failing resolver = %v, want ErrNetwork", err)
	}
	r.YouTube = nil
	if _, err := r.Resolve(t.Context(), youtubeURL); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Resolve without resolver = %v, want ErrUnavailable", err)
	}
}

func TestMatchHost(t *testing.T) {
	cases := map[string]bool{
		"metrolink.granicus.com": true,
		"METRO.GRANICUS.COM":     true,
		"granicus.com":           false,
		"evilgranicus.com":       false,
		"granicus.com.evil.test": false,
	}
	for host, want := range cases {
		if got := matchHost(DefaultHosts.Players, host); got != want {
			t.Errorf("matchHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	cases := map[error]bool{
		ErrNetwork:               true,
		ErrBlocked:               true,
		context.Canceled:         true,
		context.DeadlineExceeded: true,
		ErrRefused:               false,
		ErrNoMedia:               false,
		ErrLimitExceeded:         false,
		ErrMalformed:             false,
	}
	for err, want := range cases {
		if got := IsRetryable(err); got != want {
			t.Errorf("IsRetryable(%v) = %v, want %v", err, got, want)
		}
	}
}
