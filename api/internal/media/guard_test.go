package media

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

func pageServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func localhostURL(srv *httptest.Server) string {
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
}

func TestPageFetcherRefusesPrivateAddressesByDefault(t *testing.T) {
	hit := false
	srv := pageServer(t, func(http.ResponseWriter, *http.Request) { hit = true })
	_, err := PageFetcher{Hosts: []string{"localhost", "127.0.0.1"}, Guard: Guard{AllowPort: loopbackGuard.AllowPort}}.Fetch(t.Context(), fetch.Request{URL: srv.URL + "/MediaPlayer.php"})
	if !errors.Is(err, ErrForbiddenAddress) || fetch.IsRetryable(err) {
		t.Errorf("Fetch = %v, want a permanent ErrForbiddenAddress", err)
	}
	if hit {
		t.Error("the private address was reached")
	}
}

func TestPageFetcherChecksEveryRedirect(t *testing.T) {
	var agents []string
	var srv *httptest.Server
	srv = pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.UserAgent())
		switch r.URL.Path {
		case "/MediaPlayer.php":
			http.Redirect(w, r, localhostURL(srv)+"/player/clip/1", http.StatusFound)
		case "/away":
			http.Redirect(w, r, srv.URL+"/player/clip/1", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			_, _ = w.Write([]byte("<html>player</html>"))
		}
	})
	pages := PageFetcher{Hosts: []string{"localhost"}, Guard: loopbackGuard, UserAgent: "CivicScanner-test"}

	resp, err := pages.Fetch(t.Context(), fetch.Request{URL: localhostURL(srv) + "/MediaPlayer.php"})
	if err != nil || string(resp.Body) != "<html>player</html>" || !strings.HasSuffix(resp.FinalURL, "/player/clip/1") {
		t.Errorf("Fetch = %+v, %v", resp, err)
	}
	for _, agent := range agents {
		if agent != "CivicScanner-test" {
			t.Errorf("User-Agent = %q", agent)
		}
	}

	if _, err := pages.Fetch(t.Context(), fetch.Request{URL: localhostURL(srv) + "/away"}); !errors.Is(err, ErrForbiddenHost) {
		t.Errorf("redirect off the list = %v, want ErrForbiddenHost", err)
	}
	if _, err := pages.Fetch(t.Context(), fetch.Request{URL: localhostURL(srv) + "/loop"}); !errors.Is(err, fetch.ErrTooManyRedirects) {
		t.Errorf("redirect loop = %v, want ErrTooManyRedirects", err)
	}
	if _, err := pages.Fetch(t.Context(), fetch.Request{URL: srv.URL + "/MediaPlayer.php"}); !errors.Is(err, ErrForbiddenHost) {
		t.Errorf("host off the list = %v, want ErrForbiddenHost", err)
	}
	if _, err := pages.Fetch(t.Context(), fetch.Request{URL: "://bad"}); !errors.Is(err, fetch.ErrInvalidURL) {
		t.Errorf("bad url = %v, want ErrInvalidURL", err)
	}
}

func TestPageFetcherClassifiesStatusesAndSize(t *testing.T) {
	srv := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/busy":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/down":
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
		}
	})
	pages := PageFetcher{Hosts: []string{"localhost"}, Guard: loopbackGuard}
	cases := map[string]bool{"/busy": true, "/gone": false, "/down": true}
	for path, retryable := range cases {
		_, err := pages.Fetch(t.Context(), fetch.Request{URL: localhostURL(srv) + path})
		var fetchErr *fetch.Error
		if !errors.As(err, &fetchErr) || fetch.IsRetryable(err) != retryable {
			t.Errorf("%s: Fetch = %v, want retryable %v", path, err, retryable)
		}
		if path == "/busy" && fetchErr.RetryAfter.Seconds() != 30 {
			t.Errorf("RetryAfter = %v", fetchErr.RetryAfter)
		}
	}
	if _, err := pages.Fetch(t.Context(), fetch.Request{URL: localhostURL(srv) + "/big", MaxBytes: 1024}); !errors.Is(err, fetch.ErrTooLarge) {
		t.Errorf("large page = %v, want ErrTooLarge", err)
	}
	if resp, err := pages.Fetch(t.Context(), fetch.Request{URL: localhostURL(srv) + "/big"}); err != nil || len(resp.Body) != 2048 {
		t.Errorf("default limit: %d bytes, %v", len(resp.Body), err)
	}
}

func TestGuardChecksSchemeHostAndPort(t *testing.T) {
	hosts := []string{"archive-video.granicus.com"}
	cases := map[string]bool{
		"https://archive-video.granicus.com/a.mp4":      true,
		"https://archive-video.granicus.com:443/a.mp4":  true,
		"http://archive-video.granicus.com/a.mp4":       true,
		"http://archive-video.granicus.com:80/a.mp4":    true,
		"https://archive-video.granicus.com:8443/a.mp4": false,
		"http://archive-video.granicus.com:443/a.mp4":   false,
		"https://archive-video.granicus.com:80/a.mp4":   false,
		"https://evil.test/a.mp4":                       false,
		"httpproxy://archive-video.granicus.com/a.mp4":  false,
		"tcp://archive-video.granicus.com:443":          false,
		"file:///etc/hosts":                             false,
	}
	for raw, want := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := (Guard{}).checkURL(u, hosts); (err == nil) != want {
			t.Errorf("checkURL(%s) = %v, want allowed %v", raw, err, want)
		}
	}
	custom := Guard{AllowPort: func(scheme, port string) bool { return scheme == "http" && port == "8080" }}
	u, _ := url.Parse("http://archive-video.granicus.com:8080/a.mp4")
	if err := custom.checkURL(u, hosts); err != nil {
		t.Errorf("injected port rule = %v", err)
	}
}
