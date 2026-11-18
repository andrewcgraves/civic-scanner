package media

import (
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
)

var loopbackGuard = Guard{
	AllowAddress: func(netip.Addr) bool { return true },
	AllowPort:    func(string) bool { return true },
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
}

func generate(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "ffmpeg", append([]string{"-nostdin", "-v", "error", "-y"}, args...)...) //nolint:gosec
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate media: %v\n%s", err, out)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { //nolint:gosec
		t.Fatal(err)
	}
}

func mediaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	generate(t, dir, "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=12",
		"-c:v", "libx264", "-g", "20", "-preset", "ultrafast", "-c:a", "aac", "-shortest", "meeting.mp4")
	generate(t, dir, "-i", "meeting.mp4", "-c", "copy", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod",
		"-hls_segment_filename", "seg%d.ts", "playlist.m3u8")
	generate(t, dir, "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=3", "-c:v", "libx264", "-preset", "ultrafast", "silent.mp4")
	writeFile(t, filepath.Join(dir, "page.mp4"), "<html><body>Pardon the interruption</body></html>")
	playlist, err := os.ReadFile(filepath.Join(dir, "playlist.m3u8")) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	inflated := strings.Replace(string(playlist), "#EXTINF:4.000000,\nseg2.ts", "#EXTINF:20.000000,\nseg2.ts", 1)
	if inflated == string(playlist) {
		t.Fatalf("unexpected playlist:\n%s", playlist)
	}
	writeFile(t, filepath.Join(dir, "inflated.m3u8"), inflated)
	writeFile(t, filepath.Join(dir, "live.m3u8"), strings.Replace(string(playlist), "#EXT-X-ENDLIST", "", 1))
	writeFile(t, filepath.Join(dir, "master.m3u8"), "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100000\nplaylist.m3u8\n")
	writeFile(t, filepath.Join(dir, "self.m3u8"), "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100000\nself.m3u8\n")
	writeFile(t, filepath.Join(dir, "nested.m3u8"), "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100000\nmaster.m3u8\n")
	return dir
}

type mediaServer struct {
	*httptest.Server
	mu       sync.Mutex
	agents   []string
	failSeg  map[string]int
	requests atomic.Int64
}

func newMediaServer(t *testing.T, dir string) *mediaServer {
	t.Helper()
	s := &mediaServer{failSeg: map[string]int{}}
	files := http.FileServer(http.Dir(dir))
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		s.mu.Lock()
		s.agents = append(s.agents, r.UserAgent())
		status := s.failSeg[r.URL.Path]
		s.mu.Unlock()
		switch {
		case status != 0:
			http.Error(w, "nope", status)
		case r.URL.Path == "/redirect.mp4":
			http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound) //nolint:gosec
		default:
			files.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *mediaServer) fail(path string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failSeg[path] = status
}

func (s *mediaServer) localhost() string {
	return strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
}

func realTool(t *testing.T) Tool {
	t.Helper()
	return Tool{
		Limit:     limitBinary(t),
		Timeout:   time.Minute,
		UserAgent: "CivicScanner-test",
		Hosts:     []string{"localhost"},
		Guard:     loopbackGuard,
	}
}

func TestExtractAudioWithRealFFmpeg(t *testing.T) {
	requireFFmpeg(t)
	srv := newMediaServer(t, mediaDir(t))
	tool := realTool(t)

	for _, name := range []string{"meeting.mp4", "playlist.m3u8", "master.m3u8"} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "audio.mp3")
			if err := tool.ExtractAudio(t.Context(), srv.localhost()+"/"+name, dst); err != nil {
				t.Fatalf("ExtractAudio: %v", err)
			}
			size, err := CheckMP3(dst, 40_000)
			if err != nil {
				t.Fatalf("CheckMP3: %v", err)
			}
			if size > 80_000 {
				t.Errorf("12 s of speech-grade audio is %d bytes", size)
			}
		})
	}
	srv.mu.Lock()
	for _, agent := range srv.agents {
		if agent != "CivicScanner-test" {
			t.Errorf("request sent User-Agent %q", agent)
		}
	}
	srv.mu.Unlock()

	failures := map[string]error{
		"silent.mp4":    ErrNoAudio,
		"page.mp4":      ErrMalformed,
		"missing.mp4":   ErrRefused,
		"inflated.m3u8": ErrIncomplete,
		"live.m3u8":     ErrIncomplete,
		"self.m3u8":     ErrMalformed,
		"nested.m3u8":   ErrMalformed,
	}
	for name, want := range failures {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "audio.mp3")
			err := tool.ExtractAudio(t.Context(), srv.localhost()+"/"+name, dst)
			if !errors.Is(err, want) {
				t.Errorf("ExtractAudio = %v, want %v", err, want)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("output kept after a failure: %v", statErr)
			}
		})
	}

	t.Run("output cap", func(t *testing.T) {
		capped := tool
		capped.MaxBytes = 8192
		err := capped.ExtractAudio(t.Context(), srv.localhost()+"/meeting.mp4", filepath.Join(t.TempDir(), "audio.mp3"))
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("ExtractAudio = %v, want ErrLimitExceeded", err)
		}
	})

	t.Run("longer than the duration cap", func(t *testing.T) {
		short := tool
		short.MaxDuration = 5 * time.Second
		before := srv.requests.Load()
		err := short.ExtractAudio(t.Context(), srv.localhost()+"/playlist.m3u8", filepath.Join(t.TempDir(), "audio.mp3"))
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("ExtractAudio = %v, want ErrLimitExceeded", err)
		}
		if n := srv.requests.Load() - before; n != 1 {
			t.Errorf("requests = %d, want only the playlist", n)
		}
	})

	t.Run("local files are refused", func(t *testing.T) {
		err := tool.ExtractAudio(t.Context(), "file:///etc/hosts", filepath.Join(t.TempDir(), "audio.mp3"))
		if err == nil {
			t.Error("ExtractAudio read a local file")
		}
	})
}

func TestExtractAudioRefusesMissingAndFailingSegments(t *testing.T) {
	requireFFmpeg(t)
	dir := mediaDir(t)
	tool := realTool(t)
	cases := map[string]struct {
		path      string
		status    int
		want      error
		retryable bool
	}{
		"missing middle segment": {"/seg1.ts", http.StatusNotFound, ErrRefused, false},
		"missing last segment":   {"/seg2.ts", http.StatusNotFound, ErrRefused, false},
		"failing segment":        {"/seg1.ts", http.StatusServiceUnavailable, ErrNetwork, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newMediaServer(t, dir)
			srv.fail(tc.path, tc.status)
			dst := filepath.Join(t.TempDir(), "audio.mp3")
			err := tool.ExtractAudio(t.Context(), srv.localhost()+"/playlist.m3u8", dst)
			if !errors.Is(err, tc.want) || IsRetryable(err) != tc.retryable {
				t.Errorf("ExtractAudio = %v, want %v (retryable %v)", err, tc.want, tc.retryable)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("truncated output kept: %v", statErr)
			}
		})
	}
}

func TestExtractAudioStaysOnAllowedHosts(t *testing.T) {
	requireFFmpeg(t)
	dir := mediaDir(t)
	srv := newMediaServer(t, dir)
	tool := realTool(t)

	t.Run("redirect to another host", func(t *testing.T) {
		before := srv.requests.Load()
		target := srv.URL + "/meeting.mp4"
		err := tool.ExtractAudio(t.Context(), srv.localhost()+"/redirect.mp4?to="+target, filepath.Join(t.TempDir(), "a.mp3"))
		if !errors.Is(err, ErrForbiddenHost) || FailureKind(err) != KindUnsupported {
			t.Errorf("ExtractAudio = %v, want ErrForbiddenHost", err)
		}
		if n := srv.requests.Load() - before; n != 1 {
			t.Errorf("server saw %d requests, want only the redirect", n)
		}
	})

	t.Run("segment on another host", func(t *testing.T) {
		writeFile(t, filepath.Join(dir, "foreign.m3u8"),
			"#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\n"+srv.URL+"/seg0.ts\n#EXT-X-ENDLIST\n")
		before := srv.requests.Load()
		err := tool.ExtractAudio(t.Context(), srv.localhost()+"/foreign.m3u8", filepath.Join(t.TempDir(), "a.mp3"))
		if !errors.Is(err, ErrForbiddenHost) {
			t.Errorf("ExtractAudio = %v, want ErrForbiddenHost", err)
		}
		if n := srv.requests.Load() - before; n != 1 {
			t.Errorf("server saw %d requests, want only the playlist", n)
		}
	})

	t.Run("allowed name on a private address", func(t *testing.T) {
		public := tool
		public.Hosts = []string{"localhost", "127.0.0.1"}
		public.Guard = Guard{}
		before := srv.requests.Load()
		for _, name := range []string{"meeting.mp4", "playlist.m3u8"} {
			err := public.ExtractAudio(t.Context(), srv.localhost()+"/"+name, filepath.Join(t.TempDir(), "a.mp3"))
			if !errors.Is(err, ErrForbiddenAddress) && !errors.Is(err, ErrForbiddenHost) {
				t.Errorf("%s: ExtractAudio = %v, want a refused address", name, err)
			}
		}
		if n := srv.requests.Load() - before; n != 0 {
			t.Errorf("server saw %d requests from a guarded tool", n)
		}
	})

	t.Run("https through the tunnel", func(t *testing.T) {
		tlsSrv := httptest.NewTLSServer(http.FileServer(http.Dir(dir)))
		defer tlsSrv.Close()
		caFile := filepath.Join(t.TempDir(), "ca.pem")
		writeFile(t, caFile, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})))
		tunnel := tool
		tunnel.Hosts = []string{"127.0.0.1"}
		tunnel.Env = []string{"SSL_CERT_FILE=" + caFile}
		dst := filepath.Join(t.TempDir(), "a.mp3")
		if err := tunnel.ExtractAudio(t.Context(), tlsSrv.URL+"/meeting.mp4", dst); err != nil {
			t.Fatalf("ExtractAudio over https: %v", err)
		}
		tunnel.Hosts = []string{"localhost"}
		if err := tunnel.ExtractAudio(t.Context(), tlsSrv.URL+"/meeting.mp4", dst); !errors.Is(err, ErrForbiddenHost) {
			t.Errorf("ExtractAudio to a host off the list over https = %v, want ErrForbiddenHost", err)
		}
	})
}

func TestDownloaderResolvesAPlayerPageAndStoresOnlyAudio(t *testing.T) {
	requireFFmpeg(t)
	srv := newMediaServer(t, mediaDir(t))
	player := servingPage(`<script>video_url="`+srv.localhost()+`/playlist.m3u8"</script>`, "")
	d := Downloader{
		Resolver: Resolver{Fetcher: player, Hosts: Hosts{Players: []string{"player.test"}, Media: []string{"localhost"}}},
		Tool:     realTool(t),
	}
	dst := filepath.Join(t.TempDir(), "audio.mp3")
	audio, err := d.DownloadAudio(t.Context(), "https://player.test/MediaPlayer.php?view_id=8&clip_id=1", dst)
	if err != nil {
		t.Fatalf("DownloadAudio: %v", err)
	}
	if audio.MediaURL != srv.localhost()+"/playlist.m3u8" || audio.Via != ViaPlayer || audio.Bytes < DefaultMinBytes {
		t.Errorf("DownloadAudio = %+v", audio)
	}

	d.MinBytes = 1 << 20
	if _, err := d.DownloadAudio(t.Context(), "https://player.test/MediaPlayer.php?clip_id=1", dst); !errors.Is(err, ErrNoAudio) {
		t.Errorf("DownloadAudio below the minimum = %v, want ErrNoAudio", err)
	}
}

func TestDownloaderStopsAtResolveAndExtractErrors(t *testing.T) {
	d := Downloader{Resolver: resolver(fetchtest.Failing(&fetch.Error{StatusCode: 404, Err: errors.New("gone")}))}
	if _, err := d.DownloadAudio(t.Context(), playerURL, filepath.Join(t.TempDir(), "a.mp3")); err == nil {
		t.Error("DownloadAudio with a missing player page succeeded")
	}
	d = Downloader{Resolver: resolver(nil), Tool: Tool{}}
	if _, err := d.DownloadAudio(t.Context(), videoURL, filepath.Join(t.TempDir(), "a.mp3")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("DownloadAudio without tools = %v, want ErrUnavailable", err)
	}
}
