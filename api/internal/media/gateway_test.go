package media

import (
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func startGateway(t *testing.T, cfg GatewayConfig) *Gateway {
	t.Helper()
	if cfg.MaxRequests == 0 {
		cfg.MaxRequests = 100
	}
	gw, err := StartGateway(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	return gw
}

func get(t *testing.T, target string, header http.Header) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "read error: " + err.Error(), resp.Header
	}
	return resp.StatusCode, string(body), resp.Header
}

func TestGatewayForwardsOnlyRegisteredURLsWithItsOwnUserAgent(t *testing.T) {
	var agents []string
	srv := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.UserAgent())
		http.ServeContent(w, r, "seg.ts", time.Time{}, strings.NewReader("0123456789"))
	})
	gw := startGateway(t, GatewayConfig{Hosts: []string{"localhost"}, Guard: loopbackGuard, UserAgent: "CivicScanner-test"})
	local := gw.Register(localhostURL(srv) + "/seg0.ts?token=1")
	if !strings.HasSuffix(local, "/seg0.ts") || !strings.HasPrefix(local, "http://127.0.0.1:") {
		t.Errorf("local url = %s", local)
	}

	code, body, header := get(t, local, nil)
	if code != 200 || body != "0123456789" || header.Get("Content-Length") != "10" {
		t.Errorf("GET = %d %q %v", code, body, header)
	}
	code, body, header = get(t, local, http.Header{"Range": {"bytes=2-4"}})
	if code != http.StatusPartialContent || body != "234" || header.Get("Content-Range") != "bytes 2-4/10" {
		t.Errorf("ranged GET = %d %q %v", code, body, header)
	}
	if strings.Join(agents, ",") != "CivicScanner-test,CivicScanner-test" {
		t.Errorf("upstream user agents = %v", agents)
	}

	playlist := gw.Serve([]byte("#EXTM3U\n"), "playlist.m3u8")
	if code, body, header := get(t, playlist, nil); code != 200 || body != "#EXTM3U\n" || header.Get("Content-Type") != playlistContentType {
		t.Errorf("served playlist = %d %q %v", code, body, header)
	}
	if gw.Err() != nil {
		t.Errorf("Err = %v", gw.Err())
	}

	for _, path := range []string{"/r/99/x.ts", "/r/x/x.ts", "/other"} {
		if code, _, _ := get(t, strings.TrimSuffix(local, "/r/0/seg0.ts")+path, nil); code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", path, code)
		}
	}
	if !errors.Is(gw.Err(), ErrForbiddenHost) {
		t.Errorf("Err after unknown paths = %v", gw.Err())
	}
}

func TestGatewayRefusesWhatItMustNotFetch(t *testing.T) {
	var srv *httptest.Server
	srv = pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, srv.URL+"/seg.ts", http.StatusFound)
		case "/scheme":
			w.Header().Set("Location", r.URL.Query().Get("to"))
			w.WriteHeader(http.StatusFound)
		case "/playlist.ts":
			_, _ = w.Write([]byte("\n#EXTM3U\n#EXTINF:1,\nhttp://127.0.0.1:1/x.ts\n"))
		case "/post":
			_, _ = w.Write([]byte("ok"))
		default:
			_, _ = w.Write([]byte("segment"))
		}
	})
	cases := map[string]string{
		"redirect to another host":   localhostURL(srv) + "/away",
		"redirect to httpproxy":      localhostURL(srv) + "/scheme?to=httpproxy://localhost:1/x",
		"redirect to file":           localhostURL(srv) + "/scheme?to=file:///etc/hosts",
		"redirect to tcp":            localhostURL(srv) + "/scheme?to=tcp://localhost:1",
		"host off the list":          srv.URL + "/seg.ts",
		"playlist served as media":   localhostURL(srv) + "/playlist.ts",
		"not an http url":            "tcp://localhost:1",
		"private address by default": "",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			guard := loopbackGuard
			if target == "" {
				guard, target = Guard{AllowPort: loopbackGuard.AllowPort}, localhostURL(srv)+"/seg.ts"
			}
			gw := startGateway(t, GatewayConfig{Hosts: []string{"localhost"}, Guard: guard})
			if code, body, _ := get(t, gw.Register(target), nil); code != http.StatusForbidden {
				t.Errorf("GET = %d %q, want 403", code, body)
			}
			if err := gw.Err(); !errors.Is(err, ErrForbiddenHost) {
				t.Errorf("Err = %v, want ErrForbiddenHost", err)
			}
		})
	}

	gw := startGateway(t, GatewayConfig{Hosts: []string{"localhost"}, Guard: loopbackGuard})
	local := gw.Register(localhostURL(srv) + "/post")
	resp, err := http.Post(local, "text/plain", strings.NewReader("x")) //nolint:gosec,noctx
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST = %d, want 403", resp.StatusCode)
	}
}

func TestGatewayCountsEveryRequest(t *testing.T) {
	srv := pageServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("segment")) })
	gw := startGateway(t, GatewayConfig{Hosts: []string{"localhost"}, Guard: loopbackGuard, MaxRequests: 2})
	local := gw.Register(localhostURL(srv) + "/seg.ts")
	for i, want := range []int{200, 200, http.StatusForbidden} {
		if code, _, _ := get(t, local, nil); code != want {
			t.Errorf("request %d = %d, want %d", i, code, want)
		}
	}
	if gw.Requests() != 3 || !errors.Is(gw.Err(), ErrLimitExceeded) {
		t.Errorf("Requests = %d, Err = %v", gw.Requests(), gw.Err())
	}
}

func TestGatewayKeepsActiveTransfersAndDropsIdleOnes(t *testing.T) {
	srv := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		pause := 40 * time.Millisecond
		if r.URL.Path == "/stall.ts" {
			pause = 400 * time.Millisecond
		}
		for range 10 {
			_, _ = w.Write([]byte("x"))
			flusher.Flush()
			time.Sleep(pause)
		}
	})
	gw := startGateway(t, GatewayConfig{Hosts: []string{"localhost"}, Guard: loopbackGuard, IdleTimeout: 150 * time.Millisecond})

	start := time.Now()
	code, body, _ := get(t, gw.Register(localhostURL(srv)+"/slow.ts"), nil)
	if code != 200 || body != strings.Repeat("x", 10) {
		t.Errorf("slow but active transfer = %d %q", code, body)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("transfer took %s, want longer than twice the idle timeout", elapsed)
	}

	code, body, _ = get(t, gw.Register(localhostURL(srv)+"/stall.ts"), nil)
	if code == 200 && len(body) >= 10 {
		t.Errorf("stalled transfer completed: %d %q", code, body)
	}
}

func TestGatewayVerifiesUpstreamCertificates(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secure")) }))
	t.Cleanup(tlsSrv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(tlsSrv.Certificate())

	trusted := loopbackGuard
	trusted.RootCAs = pool
	gw := startGateway(t, GatewayConfig{Hosts: []string{"127.0.0.1"}, Guard: trusted})
	if code, body, _ := get(t, gw.Register(tlsSrv.URL+"/seg.ts"), nil); code != 200 || body != "secure" {
		t.Errorf("trusted https = %d %q", code, body)
	}

	untrusted := startGateway(t, GatewayConfig{Hosts: []string{"127.0.0.1"}, Guard: loopbackGuard})
	if code, _, _ := get(t, untrusted.Register(tlsSrv.URL+"/seg.ts"), nil); code != http.StatusBadGateway {
		t.Errorf("untrusted https = %d, want 502", code)
	}
	if untrusted.Err() != nil {
		t.Errorf("a certificate failure is not a refusal: %v", untrusted.Err())
	}
}

func TestUpstreamPath(t *testing.T) {
	cases := map[string]string{
		"https://a.test/x/seg0.ts?q=1#f": "/x/seg0.ts",
		"https://a.test":                 "/",
		"no-scheme/path":                 "/path",
	}
	for in, want := range cases {
		if got := upstreamPath(in); got != want {
			t.Errorf("upstreamPath(%q) = %q, want %q", in, got, want)
		}
	}
}
