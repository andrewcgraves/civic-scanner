package media

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

func TestPublicAddress(t *testing.T) {
	cases := map[string]bool{
		"93.184.216.34":        true,
		"2606:4700::6810:84e5": true,
		"127.0.0.1":            false,
		"::1":                  false,
		"10.1.2.3":             false,
		"172.16.0.1":           false,
		"192.168.1.1":          false,
		"169.254.169.254":      false,
		"fe80::1":              false,
		"fd12:3456::1":         false,
		"100.64.0.1":           false,
		"0.0.0.0":              false,
		"0.1.2.3":              false,
		"::":                   false,
		"224.0.0.1":            false,
		"::ffff:127.0.0.1":     false,
		"::ffff:10.0.0.1":      false,
	}
	for addr, want := range cases {
		if got := PublicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddress(%s) = %v, want %v", addr, got, want)
		}
	}
	if PublicAddress(netip.Addr{}) {
		t.Error("PublicAddress(zero) = true")
	}
}

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
	_, err := PageFetcher{Hosts: []string{"localhost", "127.0.0.1"}}.Fetch(t.Context(), fetch.Request{URL: srv.URL + "/MediaPlayer.php"})
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

func TestProxyRefusesWhatItMustNotForward(t *testing.T) {
	srv := pageServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("segment")) })
	proxy, err := StartProxy([]string{"localhost"}, loopbackGuard, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse(proxy.URL()) }}}

	get := func(target string) int {
		resp, err := client.Get(target)
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(localhostURL(srv) + "/seg0.ts"); code != http.StatusOK {
		t.Errorf("allowed host = %d", code)
	}
	if code := get(srv.URL + "/seg0.ts"); code != http.StatusForbidden {
		t.Errorf("host off the list = %d, want 403", code)
	}
	if err := proxy.Err(); !errors.Is(err, ErrForbiddenHost) {
		t.Errorf("Err = %v, want ErrForbiddenHost", err)
	}
	_ = get(localhostURL(srv) + "/seg1.ts")
	if code := get(localhostURL(srv) + "/seg2.ts"); code != http.StatusForbidden {
		t.Errorf("request past the cap = %d, want 403", code)
	}
	if proxy.Requests() != 4 {
		t.Errorf("Requests = %d", proxy.Requests())
	}

	flood, err := StartProxy([]string{"localhost"}, loopbackGuard, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer flood.Close()
	client.Transport = &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse(flood.URL()) }}
	_ = get(localhostURL(srv) + "/a")
	_ = get(localhostURL(srv) + "/b")
	if err := flood.Err(); !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("Err after the cap = %v, want ErrLimitExceeded", err)
	}

	guarded, err := StartProxy([]string{"localhost"}, Guard{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer guarded.Close()
	client.Transport = &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse(guarded.URL()) }}
	if code := get(localhostURL(srv) + "/a"); code != http.StatusForbidden {
		t.Errorf("private address = %d, want 403", code)
	}
	if err := guarded.Err(); !errors.Is(err, ErrForbiddenHost) || !strings.Contains(err.Error(), "not a public internet address") {
		t.Errorf("Err = %v", err)
	}
}
