package media

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
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

func mediaDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	generate(t, dir, "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=12",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", "meeting.mp4")
	generate(t, dir, "-i", "meeting.mp4", "-c", "copy", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod", "playlist.m3u8")
	generate(t, dir, "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=3", "-c:v", "libx264", "-preset", "ultrafast", "silent.mp4")
	if err := os.WriteFile(filepath.Join(dir, "page.mp4"), []byte("<html><body>Pardon the interruption</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func realTool(t *testing.T) Tool {
	t.Helper()
	return Tool{Limit: limitBinary(t), Timeout: time.Minute, UserAgent: "CivicScanner-test"}
}

func TestExtractAudioWithRealFFmpeg(t *testing.T) {
	requireFFmpeg(t)
	var (
		mu     sync.Mutex
		agents []string
	)
	files := http.FileServer(http.Dir(mediaDir(t)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		agents = append(agents, r.UserAgent())
		mu.Unlock()
		files.ServeHTTP(w, r)
	}))
	defer srv.Close()
	tool := realTool(t)

	for _, name := range []string{"meeting.mp4", "playlist.m3u8"} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "audio.mp3")
			if err := tool.ExtractAudio(t.Context(), srv.URL+"/"+name, dst); err != nil {
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
	mu.Lock()
	for _, agent := range agents {
		if agent != "CivicScanner-test" {
			t.Errorf("request sent User-Agent %q", agent)
		}
	}
	mu.Unlock()

	failures := map[string]error{
		"silent.mp4":  ErrNoAudio,
		"page.mp4":    ErrMalformed,
		"missing.mp4": ErrRefused,
	}
	for name, want := range failures {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "audio.mp3")
			err := tool.ExtractAudio(t.Context(), srv.URL+"/"+name, dst)
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
		err := capped.ExtractAudio(t.Context(), srv.URL+"/meeting.mp4", filepath.Join(t.TempDir(), "audio.mp3"))
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("ExtractAudio = %v, want ErrLimitExceeded", err)
		}
	})

	t.Run("duration cap", func(t *testing.T) {
		short := tool
		short.MaxDuration = 2 * time.Second
		dst := filepath.Join(t.TempDir(), "audio.mp3")
		if err := short.ExtractAudio(t.Context(), srv.URL+"/meeting.mp4", dst); err != nil {
			t.Fatalf("ExtractAudio: %v", err)
		}
		if size, err := CheckMP3(dst, 1); err != nil || size > 12_000 {
			t.Errorf("2 s of audio = %d bytes, %v", size, err)
		}
	})

	t.Run("local files are refused", func(t *testing.T) {
		err := tool.ExtractAudio(t.Context(), "file:///etc/hosts", filepath.Join(t.TempDir(), "audio.mp3"))
		if err == nil {
			t.Error("ExtractAudio read a local file")
		}
	})
}

func TestDownloaderResolvesAPlayerPageAndStoresOnlyAudio(t *testing.T) {
	requireFFmpeg(t)
	srv := httptest.NewServer(http.FileServer(http.Dir(mediaDir(t))))
	defer srv.Close()
	local := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	player := servingPage(`<script>video_url="`+srv.URL+`/playlist.m3u8"</script>`, "")
	d := Downloader{
		Resolver: Resolver{Fetcher: player, Hosts: Hosts{Players: []string{"localhost"}, Media: []string{"127.0.0.1"}}},
		Tool:     realTool(t),
	}
	dst := filepath.Join(t.TempDir(), "audio.mp3")
	audio, err := d.DownloadAudio(t.Context(), local+"/MediaPlayer.php?view_id=8&clip_id=1", dst)
	if err != nil {
		t.Fatalf("DownloadAudio: %v", err)
	}
	if audio.MediaURL != srv.URL+"/playlist.m3u8" || audio.Via != ViaPlayer || audio.Bytes < DefaultMinBytes {
		t.Errorf("DownloadAudio = %+v", audio)
	}

	d.MinBytes = 1 << 20
	if _, err := d.DownloadAudio(t.Context(), local+"/MediaPlayer.php?clip_id=1", dst); !errors.Is(err, ErrNoAudio) {
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
