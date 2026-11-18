package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
)

func limitBinary(t *testing.T) string {
	t.Helper()
	return testbin.BuildPackage(t, "../../cmd/limit")
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	return path
}

func TestExtractAudioPassesLimitsAndSafeOptionsToFFmpeg(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	tool := Tool{
		Limit:       limitBinary(t),
		FFmpeg:      script(t, `printf '%s\n' "$@" > "$ARGS_FILE"; printf 'ID3' > "$(eval echo \${$#})"`),
		Timeout:     10 * time.Second,
		MaxDuration: 90 * time.Minute,
		UserAgent:   "CivicScanner/1.0 (+https://example.test)",
		Env:         []string{"ARGS_FILE=" + argsFile},
	}
	dst := filepath.Join(t.TempDir(), "audio.mp3")
	if err := tool.ExtractAudio(t.Context(), streamURL, dst); err != nil {
		t.Fatalf("ExtractAudio: %v", err)
	}
	raw, err := os.ReadFile(argsFile) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(strings.Split(strings.TrimSpace(string(raw)), "\n"), " ")
	for _, want := range []string{
		"-protocol_whitelist " + protocolWhitelist,
		"-format_whitelist " + formatWhitelist,
		"-user_agent CivicScanner/1.0 (+https://example.test)",
		"-i " + streamURL,
		"-map 0:a:0 -vn -sn -dn -t 5400",
		"-ac 1 -ar 16000 -c:a libmp3lame -b:a 32k -f mp3 " + dst,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg args %q lack %q", args, want)
		}
	}
}

func TestExtractAudioClassifiesFailures(t *testing.T) {
	bin := limitBinary(t)
	cases := map[string]struct {
		body      string
		want      error
		retryable bool
	}{
		"server error":    {`echo "Server returned 503 Service Unavailable" >&2; exit 1`, ErrNetwork, true},
		"read timeout":    {`echo "Connection timed out" >&2; exit 1`, ErrNetwork, true},
		"forbidden":       {`echo "Server returned 403 Forbidden (access denied)" >&2; exit 1`, ErrRefused, false},
		"html page":       {`echo "Invalid data found when processing input" >&2; exit 1`, ErrMalformed, false},
		"video only":      {`echo "Stream map '0:a:0' matches no streams." >&2; exit 1`, ErrNoAudio, false},
		"out of memory":   {`echo "Cannot allocate memory" >&2; exit 1`, ErrLimitExceeded, false},
		"too much output": {`exec head -c 9000 /dev/zero > "$(eval echo \${$#})"`, ErrLimitExceeded, false},
		"crash":           {`kill -SEGV $$`, ErrMalformed, false},
		"killed":          {`kill -KILL $$`, ErrLimitExceeded, false},
		"stdout":          {`echo surprise`, ErrLimitExceeded, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tool := Tool{Limit: bin, FFmpeg: script(t, tc.body), Timeout: 10 * time.Second, MaxBytes: 4096}
			dst := filepath.Join(t.TempDir(), "audio.mp3")
			err := tool.ExtractAudio(t.Context(), streamURL, dst)
			if !errors.Is(err, tc.want) || IsRetryable(err) != tc.retryable {
				t.Errorf("ExtractAudio = %v, want %v (retryable %v)", err, tc.want, tc.retryable)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("output left behind after a failure: %v", statErr)
			}
		})
	}
}

func TestExtractAudioKillsAHungTool(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child")
	tool := Tool{Limit: limitBinary(t), FFmpeg: script(t, `(sleep 30; touch "$MARKER") & sleep 30`), Timeout: 300 * time.Millisecond, Env: []string{"MARKER=" + marker}}
	start := time.Now()
	err := tool.ExtractAudio(t.Context(), streamURL, filepath.Join(t.TempDir(), "a.mp3"))
	if !errors.Is(err, ErrLimitExceeded) || IsRetryable(err) {
		t.Errorf("ExtractAudio = %v, want a permanent ErrLimitExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s to give up", elapsed)
	}
}

func TestExtractAudioReturnsTheJobsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	tool := Tool{Limit: limitBinary(t), FFmpeg: script(t, `sleep 30`), Timeout: time.Minute}
	err := tool.ExtractAudio(ctx, streamURL, filepath.Join(t.TempDir(), "a.mp3"))
	if !errors.Is(err, context.Canceled) || !IsRetryable(err) {
		t.Errorf("ExtractAudio = %v, want a retryable context.Canceled", err)
	}
}

func TestExtractAudioUnavailableTools(t *testing.T) {
	bin := limitBinary(t)
	cases := map[string]Tool{
		"no limit binary":      {FFmpeg: "ffmpeg"},
		"missing limit binary": {Limit: "/nonexistent/limit"},
		"missing ffmpeg":       {Limit: bin, FFmpeg: "/nonexistent/ffmpeg"},
	}
	for name, tool := range cases {
		t.Run(name, func(t *testing.T) {
			err := tool.ExtractAudio(t.Context(), streamURL, filepath.Join(t.TempDir(), "a.mp3"))
			if !errors.Is(err, ErrUnavailable) || IsRetryable(err) {
				t.Errorf("ExtractAudio = %v, want a permanent ErrUnavailable", err)
			}
		})
	}
}

func TestYouTubeAudioURL(t *testing.T) {
	bin := limitBinary(t)
	argsFile := filepath.Join(t.TempDir(), "args")
	tool := Tool{
		Limit: bin,
		YTDLP: script(t, `printf '%s\n' "$@" > "$ARGS_FILE"; echo "https://rr1.googlevideo.com/videoplayback?itag=251"; echo "https://rr1.googlevideo.com/second"`),
		Env:   []string{"ARGS_FILE=" + argsFile},
	}
	got, err := tool.YouTubeAudioURL(t.Context(), youtubeURL)
	if err != nil || got != "https://rr1.googlevideo.com/videoplayback?itag=251" {
		t.Errorf("YouTubeAudioURL = %q, %v", got, err)
	}
	raw, _ := os.ReadFile(argsFile) //nolint:gosec
	if args := strings.ReplaceAll(string(raw), "\n", " "); !strings.Contains(args, "-f bestaudio --get-url -- "+youtubeURL) || !strings.Contains(args, "--ignore-config") {
		t.Errorf("yt-dlp args = %q", args)
	}

	cases := map[string]struct {
		body string
		want error
	}{
		"bot check":   {`echo "ERROR: [youtube] abc123: Sign in to confirm you're not a bot" >&2; exit 1`, ErrBlocked},
		"unavailable": {`echo "ERROR: [youtube] abc123: Video unavailable" >&2; exit 1`, ErrNoAudio},
		"no output":   {`exit 0`, ErrNoMedia},
		"flood":       {`head -c 100000 /dev/zero | tr '\0' a`, ErrLimitExceeded},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tool := Tool{Limit: bin, YTDLP: script(t, tc.body)}
			if _, err := tool.YouTubeAudioURL(t.Context(), youtubeURL); !errors.Is(err, tc.want) {
				t.Errorf("YouTubeAudioURL = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewDownloaderUsesConfigAndDefaultHosts(t *testing.T) {
	cfg := config.Audio{LimitBinary: "/opt/limit", FFmpegBinary: "/usr/bin/ffmpeg", YTDLPBinary: "/usr/bin/yt-dlp", Timeout: time.Minute, MaxDuration: time.Hour, MaxMemory: 1, MaxBytes: 2}
	d := NewDownloader(cfg, nil, "ua")
	want := Tool{Limit: "/opt/limit", FFmpeg: "/usr/bin/ffmpeg", YTDLP: "/usr/bin/yt-dlp", Timeout: time.Minute, MaxDuration: time.Hour, MaxMemory: 1, MaxBytes: 2, UserAgent: "ua"}
	if d.Tool.Limit != want.Limit || d.Tool.FFmpeg != want.FFmpeg || d.Tool.MaxBytes != 2 || d.Tool.UserAgent != "ua" || d.Resolver.YouTube == nil {
		t.Errorf("NewDownloader = %+v", d)
	}
	if len(d.Resolver.Hosts.Media) != len(DefaultHosts.Media) {
		t.Errorf("hosts = %+v", d.Resolver.Hosts)
	}
	if got := ResolveLimitBinary(""); filepath.Base(got) != "limit" {
		t.Errorf("ResolveLimitBinary = %q", got)
	}
	defaults := Tool{}.withDefaults()
	if defaults.FFmpeg != "ffmpeg" || defaults.YTDLP != "yt-dlp" || defaults.Timeout != time.Hour || defaults.MaxBytes != 256<<20 {
		t.Errorf("defaults = %+v", defaults)
	}
}

func TestCheckMP3(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	id3 := write("id3.mp3", append([]byte("ID3"), make([]byte, 100)...))
	sync := write("sync.mp3", append([]byte{0xff, 0xfb, 0x90}, make([]byte, 100)...))
	html := write("page.mp3", []byte("<html><body>"+strings.Repeat("x", 100)+"</body></html>"))
	if n, err := CheckMP3(id3, 50); err != nil || n != 103 {
		t.Errorf("CheckMP3(id3) = %d, %v", n, err)
	}
	if _, err := CheckMP3(sync, 50); err != nil {
		t.Errorf("CheckMP3(frame sync) = %v", err)
	}
	for name, p := range map[string]string{"html": html, "missing": filepath.Join(dir, "none.mp3")} {
		if _, err := CheckMP3(p, 50); !errors.Is(err, ErrNoAudio) {
			t.Errorf("CheckMP3(%s) = %v, want ErrNoAudio", name, err)
		}
	}
	if _, err := CheckMP3(id3, 1000); !errors.Is(err, ErrNoAudio) {
		t.Errorf("CheckMP3(short) = %v, want ErrNoAudio", err)
	}
}
