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

const fileURL = "https://archive-video.granicus.com/metrolink/clip.mp4"

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

func fakeTool(t *testing.T, bin, ffmpeg string) Tool {
	t.Helper()
	return Tool{
		Limit:   bin,
		FFmpeg:  script(t, ffmpeg),
		FFprobe: script(t, `echo 12.000000`),
		Timeout: 10 * time.Second,
		Hosts:   DefaultHosts.Media,
	}
}

const writeOutput = `printf 'ID3' > "$(eval echo \${$#})"`

func TestExtractAudioGivesFFmpegOnlyTheGatewayOverPlainHTTP(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	tool := fakeTool(t, limitBinary(t), `printf '%s\n' "$@" > "$ARGS_FILE"; printf 'proxy=%s|%s|%s\n' "$http_proxy" "$https_proxy" "$no_proxy" >> "$ARGS_FILE"; `+writeOutput)
	tool.MaxDuration = 90 * time.Minute
	tool.UserAgent = "CivicScanner/1.0 (+https://example.test)"
	tool.Env = []string{"ARGS_FILE=" + argsFile}
	dst := filepath.Join(t.TempDir(), "audio.mp3")
	if err := tool.ExtractAudio(t.Context(), fileURL, dst); err != nil {
		t.Fatalf("ExtractAudio: %v", err)
	}
	raw, err := os.ReadFile(argsFile) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	args := strings.Join(lines, " ")
	for _, want := range []string{
		"-loglevel warning",
		"-protocol_whitelist http,tcp ",
		"-format_whitelist " + formatWhitelist,
		"-rw_timeout 90000000",
		"-i http://127.0.0.1:",
		"/r/0/clip.mp4 -map 0:a:0 -vn -sn -dn -t 5400",
		"-ac 1 -ar 16000 -c:a libmp3lame -b:a 32k -f mp3 " + dst,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg args %q lack %q", args, want)
		}
	}
	for _, unwanted := range []string{"granicus.com", "-http_proxy", "-user_agent", "https", "httpproxy", "crypto", "file"} {
		if strings.Contains(strings.Join(lines[:len(lines)-1], " "), unwanted) && !strings.Contains(dst, unwanted) {
			t.Errorf("ffmpeg args %q contain %q", args, unwanted)
		}
	}
	if env := lines[len(lines)-1]; env != "proxy=||*" {
		t.Errorf("proxy environment = %q", env)
	}
}

func TestExtractAudioClassifiesFailures(t *testing.T) {
	bin := limitBinary(t)
	cases := map[string]struct {
		body      string
		want      error
		retryable bool
	}{
		"server error":      {`echo "Server returned 503 Service Unavailable" >&2; exit 1`, ErrNetwork, true},
		"read timeout":      {`echo "Connection timed out" >&2; exit 1`, ErrNetwork, true},
		"forbidden":         {`echo "Server returned 403 Forbidden (access denied)" >&2; exit 1`, ErrRefused, false},
		"html page":         {`echo "Invalid data found when processing input" >&2; exit 1`, ErrMalformed, false},
		"unrecognised":      {`echo "Something new went wrong" >&2; exit 1`, ErrUnrecognized, true},
		"video only":        {`echo "Stream map '0:a:0' matches no streams." >&2; exit 1`, ErrNoAudio, false},
		"out of memory":     {`echo "Cannot allocate memory" >&2; exit 1`, ErrLimitExceeded, false},
		"too much output":   {`exec head -c 9000 /dev/zero > "$(eval echo \${$#})"`, ErrLimitExceeded, false},
		"crash":             {`kill -SEGV $$`, ErrMalformed, false},
		"killed":            {`kill -KILL $$`, ErrLimitExceeded, false},
		"stdout":            {`echo surprise`, ErrLimitExceeded, false},
		"segment 404":       {`echo "[http @ 0x1] HTTP error 404 Not Found" >&2; echo "[hls @ 0x2] Failed to open segment 1 of playlist 0" >&2; ` + writeOutput, ErrRefused, false},
		"segment 503":       {`echo "[http @ 0x1] HTTP error 503 Service Unavailable" >&2; echo "[hls @ 0x2] Segment 1 of playlist 0 failed too many times, skipping" >&2; ` + writeOutput, ErrNetwork, true},
		"segment timeout":   {`echo "[tcp @ 0x1] Connection timed out" >&2; echo "[hls @ 0x2] Failed to open segment 3 of playlist 0" >&2; ` + writeOutput, ErrNetwork, true},
		"segment skipped":   {`echo "[hls @ 0x2] Failed to open segment 3 of playlist 0" >&2; ` + writeOutput, ErrIncomplete, false},
		"harmless warnings": {`echo "[mp3 @ 0x1] Estimating duration from bitrate" >&2; ` + writeOutput, nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tool := fakeTool(t, bin, tc.body)
			tool.MaxBytes = 4096
			dst := filepath.Join(t.TempDir(), "audio.mp3")
			err := tool.ExtractAudio(t.Context(), fileURL, dst)
			if tc.want == nil {
				if err != nil {
					t.Errorf("ExtractAudio = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) || IsRetryable(err) != tc.retryable {
				t.Errorf("ExtractAudio = %v, want %v (retryable %v)", err, tc.want, tc.retryable)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("output left behind after a failure: %v", statErr)
			}
		})
	}
}

func TestExtractAudioChecksTheLengthOfTheResult(t *testing.T) {
	bin := limitBinary(t)
	probe := func(source, output string) string {
		return `case "$(eval echo \${$#})" in *.mp3) echo ` + output + `;; *) printf '` + source + `';; esac`
	}
	cases := map[string]struct {
		source, output string
		want           error
	}{
		"audio stream complete":         {`600.0\n600.3\n`, "598.5", nil},
		"audio stream short":            {`600.0\n600.3\n`, "597.0", ErrIncomplete},
		"container length within slack": {`N/A\n600.0\n`, "594.5", nil},
		"container length short":        {`N/A\n600.0\n`, "590.0", ErrIncomplete},
		"format only":                   {`600.0\n`, "594.5", nil},
		"no duration at all":            {``, "1200.0", nil},
		"longer than the cap":           {`7200.0\n7200.0\n`, "3600.0", ErrLimitExceeded},
		"unknown length at the cap":     {`N/A\nN/A\n`, "3600.0", ErrLimitExceeded},
		"no output length":              {`600.0\n600.0\n`, "N/A", ErrNoAudio},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tool := fakeTool(t, bin, writeOutput)
			tool.FFprobe = script(t, probe(tc.source, tc.output))
			tool.MaxDuration = time.Hour
			err := tool.ExtractAudio(t.Context(), fileURL, filepath.Join(t.TempDir(), "audio.mp3"))
			if !errors.Is(err, tc.want) && (err != nil || tc.want != nil) {
				t.Errorf("ExtractAudio = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestExtractAudioKillsAHungTool(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child")
	tool := fakeTool(t, limitBinary(t), `(sleep 30; touch "$MARKER") & sleep 30`)
	tool.Timeout = 300 * time.Millisecond
	tool.Env = []string{"MARKER=" + marker}
	start := time.Now()
	err := tool.ExtractAudio(t.Context(), fileURL, filepath.Join(t.TempDir(), "a.mp3"))
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
	tool := fakeTool(t, limitBinary(t), `sleep 30`)
	tool.Timeout = time.Minute
	err := tool.ExtractAudio(ctx, fileURL, filepath.Join(t.TempDir(), "a.mp3"))
	if !errors.Is(err, context.Canceled) || !IsRetryable(err) {
		t.Errorf("ExtractAudio = %v, want a retryable context.Canceled", err)
	}
}

func TestExtractAudioUnavailableTools(t *testing.T) {
	bin := limitBinary(t)
	cases := map[string]Tool{
		"no limit binary":      {FFmpeg: "ffmpeg"},
		"missing limit binary": {Limit: "/nonexistent/limit", Hosts: DefaultHosts.Media},
		"missing ffprobe":      {Limit: bin, FFprobe: "/nonexistent/ffprobe", Hosts: DefaultHosts.Media},
	}
	for name, tool := range cases {
		t.Run(name, func(t *testing.T) {
			err := tool.ExtractAudio(t.Context(), fileURL, filepath.Join(t.TempDir(), "a.mp3"))
			if !errors.Is(err, ErrUnavailable) || IsRetryable(err) {
				t.Errorf("ExtractAudio = %v, want a permanent ErrUnavailable", err)
			}
		})
	}
	tool := fakeTool(t, bin, "")
	tool.FFmpeg = "/nonexistent/ffmpeg"
	if err := tool.ExtractAudio(t.Context(), fileURL, filepath.Join(t.TempDir(), "a.mp3")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("ExtractAudio with a missing ffmpeg = %v, want ErrUnavailable", err)
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
	args := strings.ReplaceAll(string(raw), "\n", " ")
	for _, want := range []string{"--ignore-config", "--use-extractors youtube", "-f bestaudio --get-url -- " + youtubeURL} {
		if !strings.Contains(args, want) {
			t.Errorf("yt-dlp args %q lack %q", args, want)
		}
	}

	cases := map[string]struct {
		tool Tool
		want error
	}{
		"bot check":   {Tool{Limit: bin, YTDLP: script(t, `echo "ERROR: [youtube] abc123: Sign in to confirm you're not a bot" >&2; exit 1`)}, ErrBlocked},
		"unavailable": {Tool{Limit: bin, YTDLP: script(t, `echo "ERROR: [youtube] abc123: Video unavailable" >&2; exit 1`)}, ErrNoAudio},
		"no output":   {Tool{Limit: bin, YTDLP: script(t, `exit 0`)}, ErrNoMedia},
		"flood":       {Tool{Limit: bin, YTDLP: script(t, `head -c 100000 /dev/zero | tr '\0' a`)}, ErrLimitExceeded},
		"no limit":    {Tool{}, ErrUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tc.tool.YouTubeAudioURL(t.Context(), youtubeURL); !errors.Is(err, tc.want) {
				t.Errorf("YouTubeAudioURL = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewDownloaderUsesConfigAndDefaultHosts(t *testing.T) {
	cfg := config.Audio{LimitBinary: "/opt/limit", FFmpegBinary: "/usr/bin/ffmpeg", FFprobeBinary: "/usr/bin/ffprobe", YTDLPBinary: "/usr/bin/yt-dlp", Timeout: time.Minute, MaxDuration: time.Hour, MaxMemory: 1, MaxBytes: 2}
	d := NewDownloader(cfg, "ua")
	if d.Tool.Limit != "/opt/limit" || d.Tool.FFmpeg != "/usr/bin/ffmpeg" || d.Tool.FFprobe != "/usr/bin/ffprobe" || d.Tool.MaxBytes != 2 ||
		d.Tool.UserAgent != "ua" || d.Resolver.YouTube == nil || len(d.Tool.Hosts) != len(DefaultHosts.Media) {
		t.Errorf("NewDownloader = %+v", d)
	}
	pages, ok := d.Resolver.Fetcher.(PageFetcher)
	if !ok || pages.UserAgent != "ua" || len(pages.Hosts) != len(DefaultHosts.Players) || pages.Guard.AllowAddress != nil {
		t.Errorf("player page fetcher = %#v", d.Resolver.Fetcher)
	}
	if got := ResolveLimitBinary(""); filepath.Base(got) != "limit" {
		t.Errorf("ResolveLimitBinary = %q", got)
	}
	defaults := Tool{}.withDefaults()
	if defaults.FFmpeg != "ffmpeg" || defaults.FFprobe != "ffprobe" || defaults.YTDLP != "yt-dlp" || defaults.Timeout != time.Hour || defaults.MaxBytes != 256<<20 {
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

func TestFailureKind(t *testing.T) {
	cases := map[error]string{
		ErrUnsupported:      KindUnsupported,
		ErrForbiddenHost:    KindUnsupported,
		ErrForbiddenAddress: KindUnsupported,
		ErrNoMedia:          KindNoMedia,
		ErrNoAudio:          KindNoAudio,
		ErrMalformed:        KindMalformed,
		ErrRefused:          KindRefused,
		ErrLimitExceeded:    KindLimit,
		ErrUnavailable:      KindUnavailable,
		ErrIncomplete:       KindIncomplete,
		ErrNetwork:          KindTransient,
		ErrBlocked:          KindTransient,
		ErrUnrecognized:     KindTransient,
		ErrInvalidURL:       KindInvalidURL,
		errors.New("other"): KindTransient,
	}
	for err, want := range cases {
		if got := FailureKind(err); got != want {
			t.Errorf("FailureKind(%v) = %q, want %q", err, got, want)
		}
	}
}
