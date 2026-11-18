package transcribe

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

type Transcript struct {
	Text     string
	Segments []Segment
	Duration time.Duration
}

type Client interface {
	Transcribe(ctx context.Context, audioPath string) (Transcript, error)
}

type groq struct {
	cfg          config.Transcription
	http         *http.Client
	splitter     Splitter
	chunkLength  time.Duration
	chunkOverlap time.Duration
}

func New(cfg config.Transcription, opts ...Option) Client {
	g := &groq{
		cfg:          cfg,
		http:         &http.Client{Timeout: cfg.Timeout},
		splitter:     FFmpeg{},
		chunkLength:  defaultChunkLength,
		chunkOverlap: defaultChunkOverlap,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

func (g *groq) Transcribe(ctx context.Context, audioPath string) (Transcript, error) {
	if g.cfg.APIKey == "" {
		return Transcript{}, ErrNotConfigured
	}
	info, err := os.Stat(audioPath)
	if err != nil {
		return Transcript{}, err
	}
	if info.Size() <= g.cfg.MaxUploadBytes {
		return g.transcribeFile(ctx, audioPath)
	}
	return g.transcribeChunked(ctx, audioPath)
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
