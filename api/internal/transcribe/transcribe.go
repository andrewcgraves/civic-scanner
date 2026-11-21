package transcribe

import (
	"context"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

type Segment struct {
	Start   time.Duration
	End     time.Duration
	Text    string
	Speaker string
}

type Word struct {
	Start   time.Duration
	End     time.Duration
	Text    string
	Speaker string
}

type Transcript struct {
	Text     string
	Segments []Segment
	Words    []Word
	Duration time.Duration
}

type Client interface {
	Transcribe(ctx context.Context, audioPath string) (Transcript, error)
}

func New(cfg config.Transcription, opts ...Option) Client {
	c := &chunker{
		upload:        newGateway(cfg),
		apiKey:        cfg.APIKey,
		splitter:      FFmpeg{},
		chunkLength:   defaultChunkLength,
		chunkOverlap:  defaultChunkOverlap,
		directBytes:   cfg.DirectUploadBytes,
		maxChunkBytes: cfg.MaxChunkBytes,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func joinText(segments []Segment, fallback string) string {
	parts := make([]string, 0, len(segments))
	for _, s := range segments {
		if s.Text != "" {
			parts = append(parts, s.Text)
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(fallback)
	}
	return strings.Join(parts, " ")
}
