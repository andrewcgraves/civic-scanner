package media

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

var _ transcribe.Splitter = Splitter{}

func TestSplitterCutsChunksUnderTheLimits(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	generate(t, dir, "-f", "lavfi", "-i", "sine=frequency=440:duration=30",
		"-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k", "meeting.mp3")
	src := filepath.Join(dir, "meeting.mp3")
	splitter := NewSplitter(realTool(t))

	total, err := splitter.Duration(t.Context(), src)
	if err != nil {
		t.Fatalf("Duration: %v", err)
	}
	if total < 29*time.Second || total > 31*time.Second {
		t.Errorf("duration = %s, want about 30s", total)
	}
	dst := filepath.Join(dir, "chunk.mp3")
	if err := splitter.Extract(t.Context(), src, dst, 10*time.Second, 10*time.Second); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	got, err := splitter.Duration(t.Context(), dst)
	if err != nil {
		t.Fatalf("chunk duration: %v", err)
	}
	if got < 9*time.Second || got > 11*time.Second {
		t.Errorf("chunk duration = %s, want about 10s", got)
	}
}

func TestSplitterRefusesNonMP3InputAndRunsOnlyUnderLimit(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	page := filepath.Join(dir, "page.mp3")
	if err := os.WriteFile(page, []byte("<html>not audio</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	splitter := NewSplitter(realTool(t))

	if err := splitter.Extract(t.Context(), page, filepath.Join(dir, "out.mp3"), 0, time.Second); err == nil {
		t.Error("Extract of an HTML page succeeded")
	}
	tool := realTool(t)
	tool.Limit = ""
	if _, err := NewSplitter(tool).Duration(t.Context(), page); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Duration without limit = %v, want ErrUnavailable", err)
	}
	if err := NewSplitter(tool).Extract(t.Context(), page, filepath.Join(dir, "out.mp3"), 0, time.Second); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Extract without limit = %v, want ErrUnavailable", err)
	}
}

func TestSplitterOutputIsBoundedByMaxBytes(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	generate(t, dir, "-f", "lavfi", "-i", "sine=frequency=440:duration=60",
		"-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k", "meeting.mp3")
	tool := realTool(t)
	tool.MaxBytes = 16 << 10

	err := NewSplitter(tool).Extract(t.Context(), filepath.Join(dir, "meeting.mp3"), filepath.Join(dir, "chunk.mp3"), 0, time.Minute)

	if !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("err = %v, want ErrLimitExceeded", err)
	}
}

func TestSplitterProbesOnlyMP3Files(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	generate(t, dir, "-f", "lavfi", "-i", "sine=frequency=440:duration=3", "meeting.wav")

	if _, err := NewSplitter(realTool(t)).Duration(t.Context(), filepath.Join(dir, "meeting.wav")); err == nil {
		t.Error("Duration of a WAV file succeeded, want only MP3 probed")
	}
}
