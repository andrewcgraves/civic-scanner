package transcribe_test

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

func TestFFmpegSplitsRealAudio(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	gen := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=6", src) //nolint:gosec
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate audio: %v: %s", err, out)
	}
	splitter := transcribe.FFmpeg{}

	total, err := splitter.Duration(t.Context(), src)
	if err != nil {
		t.Fatalf("Duration: %v", err)
	}
	if math.Abs(total.Seconds()-6) > 0.1 {
		t.Errorf("duration = %v, want about 6s", total)
	}

	dst := filepath.Join(dir, "chunk.mp3")
	if err := splitter.Extract(t.Context(), src, dst, 2*time.Second, 3*time.Second); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	chunk, err := splitter.Duration(t.Context(), dst)
	if err != nil {
		t.Fatalf("Duration of chunk: %v", err)
	}
	if math.Abs(chunk.Seconds()-3) > 0.2 {
		t.Errorf("chunk duration = %v, want about 3s", chunk)
	}
}

func fakeTool(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	return path
}

func TestFFmpegReportsMissingBinaries(t *testing.T) {
	splitter := transcribe.FFmpeg{FFmpegPath: "/nonexistent/ffmpeg", FFprobePath: "/nonexistent/ffprobe"}

	if _, err := splitter.Duration(t.Context(), "in.mp3"); err == nil {
		t.Error("Duration: expected an error")
	}
	if err := splitter.Extract(t.Context(), "in.mp3", "out.mp3", 0, time.Second); err == nil {
		t.Error("Extract: expected an error")
	}
}

func TestFFmpegIncludesToolOutputInErrors(t *testing.T) {
	splitter := transcribe.FFmpeg{
		FFmpegPath:  fakeTool(t, "echo 'Invalid data found' >&2; exit 1"),
		FFprobePath: fakeTool(t, "echo 'probe failed' >&2; exit 1"),
	}

	_, probeErr := splitter.Duration(t.Context(), "in.mp3")
	extractErr := splitter.Extract(t.Context(), "in.mp3", "out.mp3", 0, time.Second)

	if probeErr == nil || !strings.Contains(probeErr.Error(), "probe failed") {
		t.Errorf("probe error = %v", probeErr)
	}
	if extractErr == nil || !strings.Contains(extractErr.Error(), "Invalid data found") {
		t.Errorf("extract error = %v", extractErr)
	}
}

func TestFFmpegRejectsUnparsableDuration(t *testing.T) {
	for _, out := range []string{"N/A", "", "-3"} {
		splitter := transcribe.FFmpeg{FFprobePath: fakeTool(t, "echo '"+out+"'")}

		if _, err := splitter.Duration(t.Context(), "in.mp3"); err == nil {
			t.Errorf("expected an error for ffprobe output %q", out)
		}
	}
}

func TestFFmpegParsesDuration(t *testing.T) {
	splitter := transcribe.FFmpeg{FFprobePath: fakeTool(t, "echo 3725.5")}

	got, err := splitter.Duration(t.Context(), "in.mp3")

	if err != nil || got != 3725500*time.Millisecond {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestFFmpegPassesChunkWindowToFFmpeg(t *testing.T) {
	args := filepath.Join(t.TempDir(), "args")
	splitter := transcribe.FFmpeg{FFmpegPath: fakeTool(t, `echo "$@" > `+args)}

	if err := splitter.Extract(t.Context(), "in.mp3", "out.mp3", 90*time.Second, 600*time.Second); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	got, _ := os.ReadFile(filepath.Clean(args))
	want := "-nostdin -v error -y -ss 90.000 -t 600.000 -i in.mp3 -vn -map 0:a:0 -ac 1 -ar 16000 -c:a libmp3lame -b:a 32k out.mp3\n"
	if string(got) != want {
		t.Errorf("ffmpeg args = %q, want %q", got, want)
	}
}
