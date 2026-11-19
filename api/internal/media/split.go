package media

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const (
	chunkTimeout     = 5 * time.Minute
	chunkFormats     = "mp3"
	chunkMaxStderr   = 1024
	localProtocols   = "file"
	secondsPrecision = 3
)

type Splitter struct {
	Tool Tool
}

func NewSplitter(tool Tool) Splitter {
	return Splitter{Tool: tool}
}

func (s Splitter) Duration(ctx context.Context, path string) (time.Duration, error) {
	t := s.Tool.withDefaults()
	return t.localDuration(ctx, path)
}

func (s Splitter) Extract(ctx context.Context, src, dst string, start, length time.Duration) error {
	t := s.Tool.withDefaults()
	_, _, err := t.run(ctx, min(t.Timeout, chunkTimeout), chunkMaxStderr, []string{
		t.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-protocol_whitelist", localProtocols, "-format_whitelist", chunkFormats,
		"-ss", strconv.FormatFloat(start.Seconds(), 'f', secondsPrecision, 64),
		"-t", strconv.FormatFloat(length.Seconds(), 'f', secondsPrecision, 64),
		"-i", "file:" + src,
		"-map", "0:a:0", "-vn", "-sn", "-dn",
		"-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k",
		"-f", "mp3", "file:" + dst,
	}, nil, nil)
	if err != nil {
		return fmt.Errorf("extract chunk: %w", err)
	}
	return nil
}
