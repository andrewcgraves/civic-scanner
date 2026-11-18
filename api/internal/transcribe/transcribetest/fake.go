package transcribetest

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

type Fake struct {
	Respond func(audioPath string) (transcribe.Transcript, error)

	mu    sync.Mutex
	calls []string
}

func Returning(t transcribe.Transcript) *Fake {
	return &Fake{Respond: func(string) (transcribe.Transcript, error) { return t, nil }}
}

func Failing(err error) *Fake {
	return &Fake{Respond: func(string) (transcribe.Transcript, error) { return transcribe.Transcript{}, err }}
}

func (f *Fake) Transcribe(ctx context.Context, audioPath string) (transcribe.Transcript, error) {
	f.mu.Lock()
	f.calls = append(f.calls, audioPath)
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return transcribe.Transcript{}, &transcribe.Error{Retryable: true, Err: err}
	}
	if f.Respond == nil {
		return transcribe.Transcript{
			Text:     "fake transcript",
			Duration: time.Second,
			Segments: []transcribe.Segment{{End: time.Second, Text: "fake transcript"}},
		}, nil
	}
	return f.Respond(audioPath)
}

func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type Extract struct {
	Src    string
	Dst    string
	Start  time.Duration
	Length time.Duration
}

type FakeSplitter struct {
	Total      time.Duration
	ProbeErr   error
	ExtractErr error
	ChunkBytes int

	mu       sync.Mutex
	extracts []Extract
}

func (f *FakeSplitter) Duration(context.Context, string) (time.Duration, error) {
	return f.Total, f.ProbeErr
}

func (f *FakeSplitter) Extract(_ context.Context, src, dst string, start, length time.Duration) error {
	f.mu.Lock()
	f.extracts = append(f.extracts, Extract{Src: src, Dst: dst, Start: start, Length: length})
	f.mu.Unlock()

	if f.ExtractErr != nil {
		return f.ExtractErr
	}
	content := fmt.Sprintf("chunk@%s", start)
	if f.ChunkBytes > len(content) {
		content += strings.Repeat(" ", f.ChunkBytes-len(content))
	}
	return os.WriteFile(dst, []byte(content), 0o600)
}

func (f *FakeSplitter) Extracts() []Extract {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.extracts)
}
