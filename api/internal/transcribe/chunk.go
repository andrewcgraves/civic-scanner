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

type Option func(*groq)

func WithSplitter(s Splitter) Option {
	return func(g *groq) { g.splitter = s }
}

func WithChunking(length, overlap time.Duration) Option {
	return func(g *groq) { g.chunkLength, g.chunkOverlap = length, overlap }
}

func (g *groq) transcribeChunked(ctx context.Context, path string) (Transcript, error) {
	step := g.chunkLength - g.chunkOverlap
	if g.chunkOverlap < 0 || step <= 0 {
		return Transcript{}, ErrInvalidChunking
	}
	total, err := g.splitter.Duration(ctx, path)
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
		chunk, err := g.transcribeChunk(ctx, path, filepath.Join(dir, fmt.Sprintf("chunk-%d%s", index, chunkExtension)), start)
		if err != nil {
			return Transcript{}, err
		}
		last := start+g.chunkLength >= total
		lo, hi := g.ownedRange(index, step, last)
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

func (g *groq) ownedRange(index int, step time.Duration, last bool) (lo, hi time.Duration) {
	const unbounded = time.Duration(1<<63 - 1)
	lo, hi = 0, unbounded
	if index > 0 {
		lo = time.Duration(index)*step + g.chunkOverlap/2
	}
	if !last {
		hi = time.Duration(index+1)*step + g.chunkOverlap/2
	}
	return lo, hi
}

func (g *groq) transcribeChunk(ctx context.Context, src, dst string, start time.Duration) (Transcript, error) {
	if err := g.splitter.Extract(ctx, src, dst, start, g.chunkLength); err != nil {
		return Transcript{}, splitError(ctx, "extract audio chunk", err)
	}
	defer func() { _ = os.Remove(dst) }()
	info, err := os.Stat(dst)
	if err != nil {
		return Transcript{}, err
	}
	if info.Size() > g.cfg.MaxUploadBytes {
		return Transcript{}, fmt.Errorf("%w: %d bytes, limit %d", ErrChunkTooLarge, info.Size(), g.cfg.MaxUploadBytes)
	}
	return g.transcribeFile(ctx, dst)
}

func splitError(ctx context.Context, action string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &Error{Retryable: true, Err: ctxErr}
	}
	return fmt.Errorf("%s: %w", action, err)
}
