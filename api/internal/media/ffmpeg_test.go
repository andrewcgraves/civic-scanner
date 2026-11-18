package media

import (
	"bytes"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
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
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

var loopbackGuard = Guard{
	AllowAddress: func(netip.Addr) bool { return true },
	AllowPort:    func(string, string) bool { return true },
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
	writeFile(t, filepath.Join(dir, "swap.m3u8"), string(playlist))
	writeFile(t, filepath.Join(dir, "master.m3u8"), "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100000\nplaylist.m3u8\n")
	writeFile(t, filepath.Join(dir, "self.m3u8"), "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100000\nself.m3u8\n")
	writeFile(t, filepath.Join(dir, "nested.m3u8"), "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100000\nmaster.m3u8\n")
	return dir
}

func streamShapes(t *testing.T, dir string) {
	t.Helper()
	generate(t, dir, "-i", "meeting.mp4", "-c", "copy", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod",
		"-hls_flags", "single_file", "single.m3u8")
	generate(t, dir, "-i", "silent.mp4", "-c", "copy", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod",
		"-hls_segment_filename", "vseg%d.ts", "videoonly.m3u8")
	generate(t, dir, "-i", "meeting.mp4", "-vn", "-c:a", "copy", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod",
		"-hls_segment_filename", "aseg%d.ts", "audio.m3u8")
	generate(t, dir, "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=8",
		"-f", "lavfi", "-i", "sine=frequency=660:duration=8",
		"-c:v", "libx264", "-g", "20", "-preset", "ultrafast", "-c:a", "aac", "-shortest",
		"-mpegts_start_pid", "0x200", "-mpegts_pmt_start_pid", "0x1100", "-output_ts_offset", "500", "-f", "mpegts", "after.ts")
	writeFile(t, filepath.Join(dir, "codecs_master.m3u8"), "#EXTM3U\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=100,CODECS=\"avc1.42e00a\"\nvideoonly.m3u8\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=900000,CODECS=\"avc1.42e00a,mp4a.40.2\"\nplaylist.m3u8\n")
	writeFile(t, filepath.Join(dir, "nocodecs_master.m3u8"), "#EXTM3U\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=100\nvideoonly.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=900000\nplaylist.m3u8\n")
	writeFile(t, filepath.Join(dir, "shared_master.m3u8"), "#EXTM3U\n"+
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"lo\",NAME=\"en\",URI=\"audio.m3u8\"\n"+
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"hi\",NAME=\"en\",URI=\"audio.m3u8\"\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=100,AUDIO=\"lo\"\nvideoonly.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=900000,AUDIO=\"hi\"\nplaylist.m3u8\n")
	playlist, err := os.ReadFile(filepath.Join(dir, "playlist.m3u8")) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	discontinuity := strings.Replace(string(playlist), "#EXT-X-ENDLIST", "#EXT-X-DISCONTINUITY\n#EXTINF:8.000000,\nafter.ts\n#EXT-X-ENDLIST", 1)
	writeFile(t, filepath.Join(dir, "discontinuity.m3u8"), discontinuity)
}

func TestExtractAudioHandlesHLSStreamShapes(t *testing.T) {
	requireFFmpeg(t)
	dir := mediaDir(t)
	streamShapes(t, dir)
	srv := newMediaServer(t, dir)
	tool := realTool(t)
	durationOf := func(t *testing.T, path string) time.Duration {
		t.Helper()
		d, err := transcribe.FFmpeg{}.Duration(t.Context(), path)
		if err != nil {
			t.Fatalf("transcribe duration: %v", err)
		}
		return d
	}

	t.Run("single-file byte ranges are refused before any download", func(t *testing.T) {
		before := srv.requests.Load()
		err := tool.ExtractAudio(t.Context(), srv.localhost()+"/single.m3u8", filepath.Join(t.TempDir(), "a.mp3"))
		if !errors.Is(err, ErrUnsupported) || FailureKind(err) != KindUnsupported {
			t.Errorf("ExtractAudio = %v, want ErrUnsupported", err)
		}
		if n := srv.requests.Load() - before; n != 1 {
			t.Errorf("requests = %d, want only the playlist", n)
		}
	})

	for _, name := range []string{"codecs_master.m3u8", "nocodecs_master.m3u8", "shared_master.m3u8"} {
		t.Run(name, func(t *testing.T) {
			srv.mu.Lock()
			srv.paths = nil
			srv.mu.Unlock()
			dst := filepath.Join(t.TempDir(), "a.mp3")
			if err := tool.ExtractAudio(t.Context(), srv.localhost()+"/"+name, dst); err != nil {
				t.Fatalf("ExtractAudio = %v", err)
			}
			srv.mu.Lock()
			paths := strings.Join(srv.paths, " ")
			srv.mu.Unlock()
			triedVideoOnly := strings.Contains(paths, "/videoonly.m3u8")
			if wantVideoOnly := name == "nocodecs_master.m3u8"; triedVideoOnly != wantVideoOnly {
				t.Errorf("fetched %s; video-only variant tried = %v, want %v", paths, triedVideoOnly, wantVideoOnly)
			}
			if name == "shared_master.m3u8" && (!strings.Contains(paths, "/aseg0.ts") || strings.Contains(paths, "/seg0.ts")) {
				t.Errorf("fetched %s, want the shared audio rendition only", paths)
			}
			if d := durationOf(t, dst); d < 11*time.Second || d > 13*time.Second {
				t.Errorf("duration = %s, want about 12 s", d)
			}
		})
	}

	t.Run("discontinuity with new PIDs and a timestamp offset", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "a.mp3")
		if err := tool.ExtractAudio(t.Context(), srv.localhost()+"/discontinuity.m3u8", dst); err != nil {
			t.Fatalf("ExtractAudio = %v", err)
		}
		if _, err := CheckMP3(dst, DefaultMinBytes); err != nil {
			t.Errorf("CheckMP3 = %v", err)
		}
		d := durationOf(t, dst)
		if d < 19*time.Second || d > 21*time.Second {
			t.Errorf("duration = %s, want about 20 s", d)
		}
		chunk := filepath.Join(t.TempDir(), "chunk.mp3")
		if err := (transcribe.FFmpeg{}).Extract(t.Context(), dst, chunk, 10*time.Second, 8*time.Second); err != nil {
			t.Fatalf("transcribe chunk across the join: %v", err)
		}
		if c := durationOf(t, chunk); c < 7*time.Second || c > 9*time.Second {
			t.Errorf("chunk duration = %s, want about 8 s", c)
		}
		entries, err := os.ReadDir(filepath.Dir(dst))
		if err != nil || len(entries) != 1 {
			t.Errorf("left in the output directory: %v, %v", entries, err)
		}
	})

	t.Run("discontinuity runs share the size limit", func(t *testing.T) {
		capped := tool
		capped.MaxBytes = 60_000
		err := capped.ExtractAudio(t.Context(), srv.localhost()+"/discontinuity.m3u8", filepath.Join(t.TempDir(), "a.mp3"))
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("ExtractAudio = %v, want ErrLimitExceeded", err)
		}
	})

	t.Run("video-only variant is tried and dropped", func(t *testing.T) {
		writeFile(t, filepath.Join(dir, "videoonly_master.m3u8"), "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nvideoonly.m3u8\n")
		err := tool.ExtractAudio(t.Context(), srv.localhost()+"/videoonly_master.m3u8", filepath.Join(t.TempDir(), "a.mp3"))
		if !errors.Is(err, ErrNoAudio) {
			t.Errorf("ExtractAudio = %v, want ErrNoAudio", err)
		}
	})
}

type mediaServer struct {
	*httptest.Server
	mu       sync.Mutex
	agents   []string
	paths    []string
	failSeg  map[string]int
	requests atomic.Int64

	swapRequests atomic.Int64
}

func newMediaServer(t *testing.T, dir string) *mediaServer {
	t.Helper()
	s := &mediaServer{failSeg: map[string]int{}}
	files := http.FileServer(http.Dir(dir))
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		s.mu.Lock()
		s.agents = append(s.agents, r.UserAgent())
		s.paths = append(s.paths, r.URL.Path)
		status := s.failSeg[r.URL.Path]
		s.mu.Unlock()
		switch {
		case status != 0:
			http.Error(w, "nope", status)
		case r.URL.Path == "/swap.m3u8":
			if s.swapRequests.Add(1) == 1 {
				files.ServeHTTP(w, r)
				return
			}
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:4.0,\nhttpproxy://127.0.0.1:1/seg.ts\n#EXT-X-ENDLIST\n"))
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

type tcpTrap struct {
	listener net.Listener
	accepted atomic.Int64
}

func newTrap(t *testing.T) *tcpTrap {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	trap := &tcpTrap{listener: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			trap.accepted.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return trap
}

func (tr *tcpTrap) addr() string { return tr.listener.Addr().String() }

func hostileURIs(addr string) map[string]string {
	return map[string]string{
		"httpproxy": "httpproxy://" + addr + "/seg.ts",
		"https":     "https://" + addr + "/seg.ts",
		"tcp":       "tcp://" + addr,
		"tls":       "tls://" + addr,
		"crypto":    "crypto+http://" + addr + "/seg.ts",
		"concat":    "concat:http://" + addr + "/a.ts|http://" + addr + "/b.ts",
		"subfile":   "subfile,,start,0,end,0,,:http://" + addr + "/seg.ts",
		"data":      "data:video/mp2t;base64,R0dHRw==",
		"file":      "file:/etc/hosts",
		"udp":       "udp://" + addr,
	}
}

func TestFFmpegCannotReachTheNetworkExceptThroughTheGateway(t *testing.T) {
	requireFFmpeg(t)
	tool := realTool(t).withDefaults()
	for name, uri := range hostileURIs("127.0.0.1:1") {
		t.Run(name, func(t *testing.T) {
			trap := newTrap(t)
			uri := strings.ReplaceAll(uri, "127.0.0.1:1", trap.addr())
			playlists := map[string]string{
				"segment": "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\n" + uri + "\n#EXT-X-ENDLIST\n",
				"variant": "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n" + uri + "\n",
				"key":     "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"" + uri + "\"\n#EXTINF:4.0,\nhttp://" + trap.addr() + "/seg.ts\n#EXT-X-ENDLIST\n",
			}
			srv := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				_, _ = w.Write([]byte(playlists[strings.TrimPrefix(r.URL.Path, "/")]))
			})
			for kind, body := range playlists {
				piped := filepath.Join(t.TempDir(), "piped.mp3")
				_, _, _ = tool.run(t.Context(), 30*time.Second, 0, tool.ffmpegArgs(pipeArgs(), piped), noProxyEnv(), strings.NewReader(body))
				fetched := filepath.Join(t.TempDir(), "fetched.mp3")
				_, _, _ = tool.run(t.Context(), 30*time.Second, 0, tool.ffmpegArgs(tool.fileArgs(srv.URL+"/"+kind), fetched), noProxyEnv(), nil)
				for _, out := range []string{piped, fetched} {
					if _, err := CheckMP3(out, 1); err == nil {
						t.Errorf("%s %s: ffmpeg produced audio", name, kind)
					}
				}
			}
			if n := trap.accepted.Load(); n != 0 {
				t.Errorf("%s: ffmpeg opened %d connections outside the gateway", name, n)
			}
		})
	}
}

func TestExtractAudioStaysOnAllowedHosts(t *testing.T) {
	requireFFmpeg(t)
	dir := mediaDir(t)
	srv := newMediaServer(t, dir)
	trap := newTrap(t)
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

	for name, uri := range hostileURIs(trap.addr()) {
		t.Run("redirect to "+name, func(t *testing.T) {
			err := tool.ExtractAudio(t.Context(), srv.localhost()+"/redirect.mp4?to="+url.QueryEscape(uri), filepath.Join(t.TempDir(), "a.mp3"))
			if !errors.Is(err, ErrForbiddenHost) {
				t.Errorf("ExtractAudio = %v, want ErrForbiddenHost", err)
			}
		})
	}

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

	t.Run("segment that is a playlist", func(t *testing.T) {
		writeFile(t, filepath.Join(dir, "nested.ts"), "#EXTM3U\n#EXTINF:4.0,\nhttp://"+trap.addr()+"/seg.ts\n#EXT-X-ENDLIST\n")
		writeFile(t, filepath.Join(dir, "smuggle.m3u8"), "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nnested.ts\n#EXT-X-ENDLIST\n")
		err := tool.ExtractAudio(t.Context(), srv.localhost()+"/smuggle.m3u8", filepath.Join(t.TempDir(), "a.mp3"))
		if !errors.Is(err, ErrForbiddenHost) {
			t.Errorf("ExtractAudio = %v, want ErrForbiddenHost", err)
		}
	})

	t.Run("playlist swapped after inspection", func(t *testing.T) {
		before := srv.swapRequests.Load()
		dst := filepath.Join(t.TempDir(), "a.mp3")
		if err := tool.ExtractAudio(t.Context(), srv.localhost()+"/swap.m3u8", dst); err != nil {
			t.Fatalf("ExtractAudio = %v", err)
		}
		if n := srv.swapRequests.Load() - before; n != 1 {
			t.Errorf("the origin playlist was fetched %d times, want once", n)
		}
	})

	t.Run("allowed name on a private address", func(t *testing.T) {
		public := tool
		public.Hosts = []string{"localhost", "127.0.0.1"}
		public.Guard = Guard{AllowPort: loopbackGuard.AllowPort}
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

	t.Run("port off the default", func(t *testing.T) {
		strict := tool
		strict.Guard = Guard{AllowAddress: loopbackGuard.AllowAddress}
		err := strict.ExtractAudio(t.Context(), srv.localhost()+"/meeting.mp4", filepath.Join(t.TempDir(), "a.mp3"))
		if !errors.Is(err, ErrForbiddenHost) {
			t.Errorf("ExtractAudio = %v, want ErrForbiddenHost", err)
		}
	})

	if n := trap.accepted.Load(); n != 0 {
		t.Errorf("%d connections reached the trap", n)
	}
}

func TestExtractAudioOverHTTPSVerifiesCertificatesInGo(t *testing.T) {
	requireFFmpeg(t)
	dir := mediaDir(t)
	tlsSrv := httptest.NewTLSServer(http.FileServer(http.Dir(dir)))
	defer tlsSrv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(tlsSrv.Certificate())
	tool := realTool(t)
	tool.Hosts = []string{"127.0.0.1"}
	tool.Guard.RootCAs = pool
	for _, name := range []string{"meeting.mp4", "playlist.m3u8"} {
		dst := filepath.Join(t.TempDir(), "a.mp3")
		if err := tool.ExtractAudio(t.Context(), tlsSrv.URL+"/"+name, dst); err != nil {
			t.Errorf("%s over https: %v", name, err)
		}
	}

	untrusted := realTool(t)
	untrusted.Hosts = []string{"127.0.0.1"}
	err := untrusted.ExtractAudio(t.Context(), tlsSrv.URL+"/meeting.mp4", filepath.Join(t.TempDir(), "a.mp3"))
	if err == nil || FailureKind(err) != KindTransient {
		t.Errorf("untrusted certificate = %v (kind %s), want a transient failure", err, FailureKind(err))
	}
	err = untrusted.ExtractAudio(t.Context(), tlsSrv.URL+"/playlist.m3u8", filepath.Join(t.TempDir(), "a.mp3"))
	if err == nil || FailureKind(err) != KindTransient {
		t.Errorf("untrusted certificate for a playlist = %v (kind %s), want a transient failure", err, FailureKind(err))
	}
}

type slowWriter struct{ http.ResponseWriter }

func (s slowWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), 16<<10)
		time.Sleep(60 * time.Millisecond)
		m, err := s.ResponseWriter.Write(p[:n])
		written += m
		if err != nil {
			return written, err
		}
		s.ResponseWriter.(http.Flusher).Flush()
		p = p[n:]
	}
	return written, nil
}

func TestGatewayStreamsALongTransferToFFmpeg(t *testing.T) {
	requireFFmpeg(t)
	dir := mediaDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "meeting.mp4")) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(slowWriter{w}, r, "meeting.mp4", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	tool := realTool(t)
	tool.IdleTimeout = 300 * time.Millisecond
	dst := filepath.Join(t.TempDir(), "a.mp3")
	start := time.Now()
	if err := tool.ExtractAudio(t.Context(), strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)+"/meeting.mp4", dst); err != nil {
		t.Fatalf("ExtractAudio = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 600*time.Millisecond {
		t.Errorf("transfer took %s, want longer than the idle timeout", elapsed)
	}
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
