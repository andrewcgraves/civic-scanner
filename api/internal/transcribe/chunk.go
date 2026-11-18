package transcribe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultChunkLength  = 10 * time.Minute
	defaultChunkOverlap = 5 * time.Second
	chunkExtension      = ".mp3"
)

type Splitter interface {
	Duration(ctx context.Context, path string) (time.Duration, error)
	Extract(ctx context.Context, src, dst string, start, length time.Duration) error
}

type uploader interface {
	acceptsDirect(path string) bool
	transcribeFile(ctx context.Context, path string) (Transcript, error)
}

type chunker struct {
	upload        uploader
	apiKey        string
	splitter      Splitter
	chunkLength   time.Duration
	chunkOverlap  time.Duration
	directBytes   int64
	maxChunkBytes int64
}

type Option func(*chunker)

func (c *chunker) Transcribe(ctx context.Context, audioPath string) (Transcript, error) {
	if c.apiKey == "" {
		return Transcript{}, ErrNotConfigured
	}
	info, err := os.Stat(audioPath)
	if err != nil {
		return Transcript{}, err
	}
	if info.Size() <= c.directBytes && c.upload.acceptsDirect(audioPath) {
		t, err := c.upload.transcribeFile(ctx, audioPath)
		if err != nil {
			return Transcript{}, err
		}
		scopeSpeakers(t.Segments, 0)
		return t, nil
	}
	return c.transcribeChunked(ctx, audioPath)
}

func scopeSpeakers(segments []Segment, chunk int) {
	for i := range segments {
		if segments[i].Speaker != "" {
			segments[i].Speaker = fmt.Sprintf("c%d/%s", chunk, segments[i].Speaker)
		}
	}
}

func WithSplitter(s Splitter) Option {
	return func(c *chunker) { c.splitter = s }
}

func WithChunking(length, overlap time.Duration) Option {
	return func(c *chunker) { c.chunkLength, c.chunkOverlap = length, overlap }
}

func (c *chunker) transcribeChunked(ctx context.Context, path string) (Transcript, error) {
	step := c.chunkLength - c.chunkOverlap
	if c.chunkOverlap < 0 || step <= 0 {
		return Transcript{}, ErrInvalidChunking
	}
	total, err := c.splitter.Duration(ctx, path)
	if err != nil {
		return Transcript{}, splitError(ctx, "probe audio duration", err)
	}
	dir, err := os.MkdirTemp("", "transcribe-*")
	if err != nil {
		return Transcript{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var segments []Segment
	for index, start := 0, time.Duration(0); ; index, start = index+1, start+step {
		chunk, err := c.transcribeChunk(ctx, path, filepath.Join(dir, fmt.Sprintf("chunk-%d%s", index, chunkExtension)), start)
		if err != nil {
			return Transcript{}, err
		}
		scopeSpeakers(chunk.Segments, index)
		last := start+c.chunkLength >= total
		lo, hi := c.ownedRange(index, step, last)
		for _, s := range chunk.Segments {
			s.Start += start
			s.End += start
			if mid := s.Start + (s.End-s.Start)/2; mid >= lo && mid < hi {
				segments = append(segments, s)
			}
		}
		if last {
			break
		}
	}
	return Transcript{Text: joinText(segments, ""), Segments: segments, Duration: total}, nil
}

func (c *chunker) ownedRange(index int, step time.Duration, last bool) (lo, hi time.Duration) {
	const unbounded = time.Duration(1<<63 - 1)
	lo, hi = 0, unbounded
	if index > 0 {
		lo = time.Duration(index)*step + c.chunkOverlap/2
	}
	if !last {
		hi = time.Duration(index+1)*step + c.chunkOverlap/2
	}
	return lo, hi
}

func (c *chunker) transcribeChunk(ctx context.Context, src, dst string, start time.Duration) (Transcript, error) {
	if err := c.splitter.Extract(ctx, src, dst, start, c.chunkLength); err != nil {
		return Transcript{}, splitError(ctx, "extract audio chunk", err)
	}
	defer func() { _ = os.Remove(dst) }()
	info, err := os.Stat(dst)
	if err != nil {
		return Transcript{}, err
	}
	if info.Size() > c.maxChunkBytes {
		return Transcript{}, fmt.Errorf("%w: %d bytes, limit %d", ErrChunkTooLarge, info.Size(), c.maxChunkBytes)
	}
	return c.upload.transcribeFile(ctx, dst)
}

func splitError(ctx context.Context, action string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &Error{Retryable: true, Err: ctxErr}
	}
	return fmt.Errorf("%s: %w", action, err)
}
