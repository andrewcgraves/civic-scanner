package transcribe

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const maxToolOutputBytes = 2048

type FFmpeg struct {
	FFmpegPath  string
	FFprobePath string
}

func (f FFmpeg) Duration(ctx context.Context, path string) (time.Duration, error) {
	out, err := run(ctx, orDefault(f.FFprobePath, "ffprobe"),
		"-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(out)
	secs, err := strconv.ParseFloat(value, 64)
	if err != nil || secs <= 0 || math.IsInf(secs, 0) || math.IsNaN(secs) {
		return 0, fmt.Errorf("ffprobe reported an unusable duration %q", value)
	}
	return seconds(secs), nil
}

func (f FFmpeg) Extract(ctx context.Context, src, dst string, start, length time.Duration) error {
	_, err := run(ctx, orDefault(f.FFmpegPath, "ffmpeg"),
		"-nostdin", "-v", "error", "-y",
		"-ss", fmt.Sprintf("%.3f", start.Seconds()),
		"-t", fmt.Sprintf("%.3f", length.Seconds()),
		"-i", src,
		"-vn", "-map", "0:a:0", "-ac", "1", "-ar", "16000",
		"-c:a", "libmp3lame", "-b:a", "32k",
		dst)
	return err
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > maxToolOutputBytes {
			detail = detail[len(detail)-maxToolOutputBytes:]
		}
		return "", fmt.Errorf("%s: %w: %s", name, err, detail)
	}
	return stdout.String(), nil
}
