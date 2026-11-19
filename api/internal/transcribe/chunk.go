package transcribe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultChunkLength  = 10 * time.Minute
	defaultChunkOverlap = 30 * time.Second
	chunkExtension      = ".mp3"
	defaultRetryDelay   = 5 * time.Second
	maxRetryDelay       = time.Minute
	MaxConcurrency      = 8
)

var ErrTooLong = errors.New("transcribe: audio is longer than the limit")

type Splitter interface {
	Duration(ctx context.Context, path string) (time.Duration, error)
	Extract(ctx context.Context, src, dst string, start, length time.Duration) error
}

type Chunk struct {
	Index  int
	Start  time.Duration
	Length time.Duration
}

type Checkpoint interface {
	Load(ctx context.Context, c Chunk) (Transcript, bool)
	Save(ctx context.Context, c Chunk, t Transcript)
}

type Options struct {
	MaxDuration time.Duration
	Concurrency int
	Retries     int
	RetryDelay  time.Duration
	Checkpoint  Checkpoint
	TempDir     string
}

type Resumable interface {
	Client
	TranscribeWith(ctx context.Context, audioPath string, opts Options) (Transcript, error)
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
	return c.TranscribeWith(ctx, audioPath, Options{})
}

func (c *chunker) TranscribeWith(ctx context.Context, audioPath string, opts Options) (Transcript, error) {
	if c.apiKey == "" {
		return Transcript{}, ErrNotConfigured
	}
	info, err := os.Stat(audioPath)
	if err != nil {
		return Transcript{}, err
	}
	r := &pass{chunker: c, opts: opts.withDefaults(), retriesLeft: opts.Retries}
	if info.Size() <= c.directBytes && c.upload.acceptsDirect(audioPath) {
		if opts.MaxDuration > 0 {
			if _, err := r.probe(ctx, audioPath); err != nil {
				return Transcript{}, err
			}
		}
		t, err := r.call(ctx, audioPath)
		if err != nil {
			return Transcript{}, err
		}
		scopeSpeakers(t.Segments, 0)
		return t, nil
	}
	return r.transcribeChunked(ctx, audioPath)
}

func (o Options) withDefaults() Options {
	o.Concurrency = min(max(o.Concurrency, 1), MaxConcurrency)
	if o.RetryDelay <= 0 {
		o.RetryDelay = defaultRetryDelay
	}
	return o
}

func scopeSpeakers(segments []Segment, chunk int) {
	for i := range segments {
		if segments[i].Speaker != "" {
			segments[i].Speaker = fmt.Sprintf("c%d/%s", chunk, segments[i].Speaker)
		}
	}
}

func scopeWordSpeakers(words []Word, chunk int) {
	for i := range words {
		if words[i].Speaker != "" {
			words[i].Speaker = fmt.Sprintf("c%d/%s", chunk, words[i].Speaker)
		}
	}
}

func WithSplitter(s Splitter) Option {
	return func(c *chunker) { c.splitter = s }
}

func WithChunking(length, overlap time.Duration) Option {
	return func(c *chunker) { c.chunkLength, c.chunkOverlap = length, overlap }
}

type pass struct {
	*chunker
	opts Options

	mu          sync.Mutex
	retriesLeft int
}

func (r *pass) probe(ctx context.Context, path string) (time.Duration, error) {
	total, err := r.splitter.Duration(ctx, path)
	if err != nil {
		return 0, splitError(ctx, "probe audio duration", err)
	}
	if r.opts.MaxDuration > 0 && total > r.opts.MaxDuration {
		return 0, fmt.Errorf("%w: %s, limit %s", ErrTooLong, total.Round(time.Second), r.opts.MaxDuration)
	}
	return total, nil
}

func (r *pass) plan(total time.Duration) []Chunk {
	step := r.chunkLength - r.chunkOverlap
	var chunks []Chunk
	for index, start := 0, time.Duration(0); ; index, start = index+1, start+step {
		chunks = append(chunks, Chunk{Index: index, Start: start, Length: r.chunkLength})
		if start+r.chunkLength >= total {
			return chunks
		}
	}
}

func (r *pass) transcribeChunked(ctx context.Context, path string) (Transcript, error) {
	step := r.chunkLength - r.chunkOverlap
	if r.chunkOverlap < 0 || step <= 0 {
		return Transcript{}, ErrInvalidChunking
	}
	total, err := r.probe(ctx, path)
	if err != nil {
		return Transcript{}, err
	}
	chunks := r.plan(total)
	dir, err := os.MkdirTemp(r.opts.TempDir, "transcribe-*")
	if err != nil {
		return Transcript{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	results, err := r.transcribeAll(ctx, path, dir, chunks)
	if err != nil {
		return Transcript{}, err
	}
	for i, chunk := range chunks {
		scopeSpeakers(results[i].Segments, chunk.Index)
		scopeWordSpeakers(results[i].Words, chunk.Index)
	}
	if textOnly(results) {
		texts := make([]string, len(results))
		for i, t := range results {
			texts[i] = t.Text
		}
		return Transcript{Text: stitchText(texts), Duration: total}, nil
	}
	segments := stitchSegments(chunks, results)
	return Transcript{Text: joinText(segments, ""), Segments: segments, Duration: total}, nil
}

func textOnly(results []Transcript) bool {
	for _, t := range results {
		if len(t.Segments) == 0 && strings.TrimSpace(t.Text) != "" {
			return true
		}
	}
	return false
}

type firstError struct {
	mu  sync.Mutex
	err error
}

func (f *firstError) set(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err == nil {
		f.err = err
	}
}

func (f *firstError) get() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (r *pass) transcribeAll(ctx context.Context, path, dir string, chunks []Chunk) ([]Transcript, error) {
	results := make([]Transcript, len(chunks))
	slots := make(chan struct{}, r.opts.Concurrency)
	var (
		wg     sync.WaitGroup
		failed firstError
	)
	for i, chunk := range chunks {
		if failed.get() != nil {
			break
		}
		if !acquire(ctx, slots) {
			failed.set(&Error{Retryable: true, Err: ctx.Err()})
			break
		}
		if failed.get() != nil {
			<-slots
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			dst := filepath.Join(dir, fmt.Sprintf("chunk-%d%s", chunk.Index, chunkExtension))
			t, err := r.chunkTranscript(ctx, path, dst, chunk)
			if err != nil {
				failed.set(err)
				return
			}
			results[i] = t
		})
	}
	wg.Wait()
	if err := failed.get(); err != nil {
		return nil, err
	}
	return results, nil
}

func acquire(ctx context.Context, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *pass) chunkTranscript(ctx context.Context, src, dst string, chunk Chunk) (Transcript, error) {
	if r.opts.Checkpoint != nil {
		if t, ok := r.opts.Checkpoint.Load(ctx, chunk); ok {
			return t, nil
		}
	}
	if err := r.splitter.Extract(ctx, src, dst, chunk.Start, chunk.Length); err != nil {
		return Transcript{}, splitError(ctx, "extract audio chunk", err)
	}
	defer func() { _ = os.Remove(dst) }()
	info, err := os.Stat(dst)
	if err != nil {
		return Transcript{}, err
	}
	if info.Size() > r.maxChunkBytes {
		return Transcript{}, fmt.Errorf("%w: %d bytes, limit %d", ErrChunkTooLarge, info.Size(), r.maxChunkBytes)
	}
	t, err := r.call(ctx, dst)
	if err != nil {
		return Transcript{}, err
	}
	if r.opts.Checkpoint != nil {
		r.opts.Checkpoint.Save(ctx, chunk, t)
	}
	return t, nil
}

func (r *pass) call(ctx context.Context, path string) (Transcript, error) {
	for attempt := 0; ; attempt++ {
		t, err := r.upload.transcribeFile(ctx, path)
		if err == nil {
			return t, nil
		}
		if !IsRetryable(err) || ctx.Err() != nil || !r.takeRetry() {
			return Transcript{}, err
		}
		timer := time.NewTimer(r.retryDelay(err, attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return Transcript{}, &Error{Retryable: true, Err: ctx.Err()}
		case <-timer.C:
		}
	}
}

func (r *pass) takeRetry() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retriesLeft <= 0 {
		return false
	}
	r.retriesLeft--
	return true
}

func (r *pass) retryDelay(err error, attempt int) time.Duration {
	var e *Error
	if errors.As(err, &e) && e.RetryAfter > 0 {
		return min(e.RetryAfter, maxRetryDelay)
	}
	return min(r.opts.RetryDelay<<min(attempt, 10), maxRetryDelay)
}

func splitError(ctx context.Context, action string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &Error{Retryable: true, Err: ctxErr}
	}
	return fmt.Errorf("%s: %w", action, err)
}
