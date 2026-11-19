package transcribe_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe/transcribetest"
)

type memoryCheckpoint struct {
	mu     sync.Mutex
	chunks map[transcribe.Chunk]transcribe.Transcript
	loads  int
	saves  []transcribe.Chunk
}

func newMemoryCheckpoint() *memoryCheckpoint {
	return &memoryCheckpoint{chunks: map[transcribe.Chunk]transcribe.Transcript{}}
}

func (m *memoryCheckpoint) Load(_ context.Context, c transcribe.Chunk) (transcribe.Transcript, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	t, ok := m.chunks[c]
	return t, ok
}

func (m *memoryCheckpoint) Save(_ context.Context, c transcribe.Chunk, t transcribe.Transcript) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks[c] = t
	m.saves = append(m.saves, c)
}

func (m *memoryCheckpoint) saved() []transcribe.Chunk {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]transcribe.Chunk(nil), m.saves...)
}

func resumable(t *testing.T, url string, splitter transcribe.Splitter) transcribe.Resumable {
	t.Helper()
	client, ok := chunkedClient(chunkConfig(url), splitter).(transcribe.Resumable)
	if !ok {
		t.Fatal("the chunking client is not resumable")
	}
	return client
}

var threeChunks = map[string][]replySegment{
	"chunk@0s":  {{Start: 0, End: 4, Text: " one"}},
	"chunk@8s":  {{Start: 1, End: 4, Text: " two"}},
	"chunk@16s": {{Start: 1, End: 3, Text: " three"}},
}

func TestRetryAfterPartialProgressReusesCheckpointedChunks(t *testing.T) {
	var failChunk atomic.Value
	failChunk.Store("chunk@16s")
	var uploads []string
	var mu sync.Mutex
	srv := chunkServer(t, threeChunks, func(chunk string, _ int) int {
		mu.Lock()
		uploads = append(uploads, chunk)
		mu.Unlock()
		if chunk == failChunk.Load() {
			return http.StatusBadGateway
		}
		return 0
	})
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second}
	client := resumable(t, srv.URL, splitter)
	checkpoint := newMemoryCheckpoint()
	opts := transcribe.Options{Checkpoint: checkpoint}

	_, err := client.TranscribeWith(t.Context(), bigAudio(t), opts)
	if !transcribe.IsRetryable(err) {
		t.Fatalf("first pass err = %v, want retryable", err)
	}
	if got := checkpoint.saved(); len(got) != 2 {
		t.Fatalf("checkpointed chunks after the failure = %+v, want the two that succeeded", got)
	}

	failChunk.Store("")
	mu.Lock()
	uploads = nil
	mu.Unlock()
	got, err := client.TranscribeWith(t.Context(), bigAudio(t), opts)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got.Text != "one two three" {
		t.Errorf("text = %q", got.Text)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(uploads) != 1 || uploads[0] != "chunk@16s" {
		t.Errorf("uploads on the second pass = %q, want only the chunk that failed", uploads)
	}
	if extracts := splitter.Extracts(); len(extracts) != 4 {
		t.Errorf("extracts = %d, want 3 then 1", len(extracts))
	}
}

func TestATransientChunkFailureIsRetriedInPlaceWithinTheBudget(t *testing.T) {
	var failures atomic.Int32
	srv := chunkServer(t, threeChunks, func(chunk string, _ int) int {
		if chunk == "chunk@16s" && failures.Add(1) <= 2 {
			return http.StatusServiceUnavailable
		}
		return 0
	})
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second}

	got, err := resumable(t, srv.URL, splitter).TranscribeWith(t.Context(), bigAudio(t),
		transcribe.Options{Retries: 2, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("TranscribeWith: %v", err)
	}
	if got.Text != "one two three" || len(splitter.Extracts()) != 3 {
		t.Errorf("text = %q, extracts = %d; want every chunk extracted once", got.Text, len(splitter.Extracts()))
	}
}

func TestTheRetryBudgetIsSharedAcrossChunks(t *testing.T) {
	var calls atomic.Int32
	srv := chunkServer(t, threeChunks, func(string, int) int {
		calls.Add(1)
		return http.StatusTooManyRequests
	})
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second}

	_, err := resumable(t, srv.URL, splitter).TranscribeWith(t.Context(), bigAudio(t),
		transcribe.Options{Retries: 2, RetryDelay: time.Millisecond})

	if !transcribe.IsRetryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 1 plus 2 retries", n)
	}
}

func TestPermanentErrorsAreNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := chunkServer(t, threeChunks, func(string, int) int {
		calls.Add(1)
		return http.StatusBadRequest
	})
	defer srv.Close()

	_, err := resumable(t, srv.URL, &transcribetest.FakeSplitter{Total: 20 * time.Second}).TranscribeWith(t.Context(), bigAudio(t),
		transcribe.Options{Retries: 5, RetryDelay: time.Millisecond})

	if err == nil || transcribe.IsRetryable(err) || calls.Load() != 1 {
		t.Errorf("err = %v after %d calls, want one permanent failure", err, calls.Load())
	}
}

func TestRetryAfterIsHonouredAndCancellationStopsTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		cancel()
	}))
	defer srv.Close()
	started := time.Now()

	_, err := resumable(t, srv.URL, &transcribetest.FakeSplitter{Total: 20 * time.Second}).TranscribeWith(ctx, bigAudio(t),
		transcribe.Options{Retries: 3})

	if !transcribe.IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want a retryable cancellation", err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("took %s: the wait ignored cancellation", took)
	}
}

func TestConcurrentChunksStitchLikeSequentialOnesAndStayBounded(t *testing.T) {
	var inFlight, peak atomic.Int32
	segments := map[string][]replySegment{}
	for _, start := range []string{"0s", "8s", "16s", "24s", "32s", "40s"} {
		segments["chunk@"+start] = []replySegment{{Start: 1, End: 3, Text: " at " + start}}
	}
	srv := chunkServer(t, segments, func(string, int) int {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		return 0
	})
	defer srv.Close()

	sequential, err := resumable(t, srv.URL, &transcribetest.FakeSplitter{Total: 48 * time.Second}).TranscribeWith(t.Context(), bigAudio(t), transcribe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	peak.Store(0)
	concurrent, err := resumable(t, srv.URL, &transcribetest.FakeSplitter{Total: 48 * time.Second}).TranscribeWith(t.Context(), bigAudio(t),
		transcribe.Options{Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}

	assertTranscript(t, concurrent, sequential)
	if sequential.Text != "at 0s at 8s at 16s at 24s at 32s at 40s" {
		t.Errorf("text = %q", sequential.Text)
	}
	if p := peak.Load(); p < 2 || p > 3 {
		t.Errorf("peak concurrent uploads = %d, want 2 or 3", p)
	}
}

func TestAFailureStopsNewChunksButKeepsInFlightResults(t *testing.T) {
	segments := map[string][]replySegment{}
	for _, start := range []string{"0s", "8s", "16s", "24s", "32s", "40s"} {
		segments["chunk@"+start] = []replySegment{{Start: 1, End: 3, Text: " at " + start}}
	}
	srv := chunkServer(t, segments, func(chunk string, _ int) int {
		if chunk == "chunk@0s" {
			return http.StatusBadRequest
		}
		time.Sleep(50 * time.Millisecond)
		return 0
	})
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 48 * time.Second}
	checkpoint := newMemoryCheckpoint()

	_, err := resumable(t, srv.URL, splitter).TranscribeWith(t.Context(), bigAudio(t),
		transcribe.Options{Concurrency: 2, Checkpoint: checkpoint})

	if err == nil {
		t.Fatal("want the permanent failure")
	}
	if n := len(splitter.Extracts()); n > 3 {
		t.Errorf("extracts = %d, want no new chunks after the failure", n)
	}
	if saved := checkpoint.saved(); len(saved) != len(splitter.Extracts())-1 {
		t.Errorf("checkpointed %d of %d extracted chunks, want every chunk that finished", len(saved), len(splitter.Extracts()))
	}
}

func TestAudioOverTheDurationLimitIsRefusedBeforeAnyUpload(t *testing.T) {
	var calls atomic.Int32
	srv := chunkServer(t, nil, func(string, int) int { calls.Add(1); return 0 })
	defer srv.Close()
	cases := map[string]string{"chunked": "big", "direct": "tiny"}
	for name, size := range cases {
		t.Run(name, func(t *testing.T) {
			splitter := &transcribetest.FakeSplitter{Total: 9 * time.Hour}
			path := bigAudio(t)
			if size == "tiny" {
				path = audioFile(t, "tiny")
			}

			_, err := resumable(t, srv.URL, splitter).TranscribeWith(t.Context(), path, transcribe.Options{MaxDuration: 8 * time.Hour})

			if !errors.Is(err, transcribe.ErrTooLong) || transcribe.IsRetryable(err) {
				t.Errorf("err = %v, want ErrTooLong", err)
			}
			if len(splitter.Extracts()) != 0 || calls.Load() != 0 {
				t.Errorf("extracts = %d, uploads = %d, want none", len(splitter.Extracts()), calls.Load())
			}
		})
	}
}

func TestCancelledBeforeAChunkSlotIsFreeIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	srv := chunkServer(t, threeChunks, nil)
	defer srv.Close()

	_, err := resumable(t, srv.URL, &transcribetest.FakeSplitter{Total: 20 * time.Second}).TranscribeWith(ctx, bigAudio(t),
		transcribe.Options{Concurrency: 1})

	if !transcribe.IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
