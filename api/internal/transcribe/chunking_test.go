package transcribe_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe/transcribetest"
)

type reply struct {
	Duration float64        `json:"duration"`
	Text     string         `json:"text"`
	Segments []replySegment `json:"segments"`
}

type replySegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

func chunkServer(t *testing.T, byChunk map[string][]replySegment, hook func(chunk string, n int) (status int)) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	n := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil { //nolint:gosec
			t.Errorf("ParseMultipartForm: %v", err)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
			return
		}
		b, _ := io.ReadAll(f)
		chunk := string(b)
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		if hook != nil {
			if status := hook(chunk, call); status != 0 {
				w.WriteHeader(status)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(reply{Duration: 10, Segments: byChunk[chunk]})
	}))
}

func chunkConfig(url string) config.Transcription {
	cfg := testConfig(url)
	cfg.DirectUploadBytes, cfg.MaxChunkBytes = 64, 64
	return cfg
}

func bigAudio(t *testing.T) string {
	t.Helper()
	return audioFile(t, strings.Repeat("x", 100))
}

func chunkedClient(cfg config.Transcription, splitter transcribe.Splitter) transcribe.Client {
	return transcribe.New(cfg, transcribe.WithSplitter(splitter), transcribe.WithChunking(10*time.Second, 2*time.Second))
}

func assertEmptyTempDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func isolatedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func TestChunkedTranscriptShiftsTimestampsAndDropsOverlap(t *testing.T) {
	tmp := isolatedTempDir(t)
	srv := chunkServer(t, map[string][]replySegment{
		"chunk@0s": {
			{Start: 0, End: 4, Text: " one"},
			{Start: 4, End: 8.8, Text: " two"},
			{Start: 8.8, End: 10, Text: " thre"},
		},
		"chunk@8s": {
			{Start: 0, End: 0.8, Text: " two"},
			{Start: 0.8, End: 4, Text: " three"},
			{Start: 4, End: 8.8, Text: " four"},
			{Start: 8.8, End: 9.9, Text: " fiv"},
		},
		"chunk@16s": {
			{Start: 0.8, End: 2, Text: " five"},
			{Start: 2, End: 4, Text: " six"},
		},
	}, nil)
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second}

	src := bigAudio(t)
	got, err := chunkedClient(chunkConfig(srv.URL), splitter).Transcribe(t.Context(), src)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	ms := time.Millisecond
	assertTranscript(t, got, transcribe.Transcript{
		Text:     "one two three four five six",
		Duration: 20 * time.Second,
		Segments: []transcribe.Segment{
			{Start: 0, End: 4000 * ms, Text: "one"},
			{Start: 4000 * ms, End: 8800 * ms, Text: "two"},
			{Start: 8800 * ms, End: 12000 * ms, Text: "three"},
			{Start: 12000 * ms, End: 16800 * ms, Text: "four"},
			{Start: 16800 * ms, End: 18000 * ms, Text: "five"},
			{Start: 18000 * ms, End: 20000 * ms, Text: "six"},
		},
	})
	extracts := splitter.Extracts()
	if len(extracts) != 3 {
		t.Fatalf("extracts = %d, want 3", len(extracts))
	}
	for i, start := range []time.Duration{0, 8 * time.Second, 16 * time.Second} {
		if extracts[i].Src != src || extracts[i].Start != start || extracts[i].Length != 10*time.Second {
			t.Errorf("extract %d = %+v", i, extracts[i])
		}
	}
	assertEmptyTempDir(t, tmp)
}

func TestSmallFileIsSentWholeWithoutSplitting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, verboseReply)
	}))
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: time.Hour}

	got, err := chunkedClient(chunkConfig(srv.URL), splitter).Transcribe(t.Context(), audioFile(t, "tiny"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if got.Duration != 12500*time.Millisecond || len(splitter.Extracts()) != 0 {
		t.Errorf("duration = %v, extracts = %d", got.Duration, len(splitter.Extracts()))
	}
}

func TestShortFileOverTheLimitStillMakesOneChunk(t *testing.T) {
	srv := chunkServer(t, map[string][]replySegment{"chunk@0s": {{Start: 0, End: 3, Text: " only"}}}, nil)
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 6 * time.Second}

	got, err := chunkedClient(chunkConfig(srv.URL), splitter).Transcribe(t.Context(), bigAudio(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	assertTranscript(t, got, transcribe.Transcript{
		Text:     "only",
		Duration: 6 * time.Second,
		Segments: []transcribe.Segment{{End: 3 * time.Second, Text: "only"}},
	})
}

func TestChunkFailureCleansUpAndKeepsClassification(t *testing.T) {
	tmp := isolatedTempDir(t)
	srv := chunkServer(t, nil, func(_ string, n int) int {
		if n == 2 {
			return http.StatusServiceUnavailable
		}
		return 0
	})
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second}

	_, err := chunkedClient(chunkConfig(srv.URL), splitter).Transcribe(t.Context(), bigAudio(t))

	if !transcribe.IsRetryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
	if len(splitter.Extracts()) != 2 {
		t.Errorf("extracts = %d, want 2: no work after the failure", len(splitter.Extracts()))
	}
	assertEmptyTempDir(t, tmp)
}

func TestCancellationMidwayCleansUp(t *testing.T) {
	tmp := isolatedTempDir(t)
	ctx, cancel := context.WithCancel(t.Context())
	srv := chunkServer(t, nil, func(_ string, n int) int {
		if n == 2 {
			cancel()
			time.Sleep(50 * time.Millisecond)
		}
		return 0
	})
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 30 * time.Second}

	_, err := chunkedClient(chunkConfig(srv.URL), splitter).Transcribe(ctx, bigAudio(t))

	if !transcribe.IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	assertEmptyTempDir(t, tmp)
}

func TestChunkOverTheUploadLimitIsPermanent(t *testing.T) {
	tmp := isolatedTempDir(t)
	srv := chunkServer(t, nil, nil)
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second, ChunkBytes: 200}

	_, err := chunkedClient(chunkConfig(srv.URL), splitter).Transcribe(t.Context(), bigAudio(t))

	if !errors.Is(err, transcribe.ErrChunkTooLarge) || transcribe.IsRetryable(err) {
		t.Errorf("err = %v", err)
	}
	assertEmptyTempDir(t, tmp)
}

func TestSplitterFailuresAreClassified(t *testing.T) {
	boom := errors.New("ffmpeg exploded")
	cases := map[string]struct {
		splitter *transcribetest.FakeSplitter
	}{
		"probe":   {&transcribetest.FakeSplitter{ProbeErr: boom}},
		"extract": {&transcribetest.FakeSplitter{Total: 20 * time.Second, ExtractErr: boom}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := isolatedTempDir(t)
			_, err := chunkedClient(chunkConfig("http://127.0.0.1:1"), tc.splitter).Transcribe(t.Context(), bigAudio(t))

			if !errors.Is(err, boom) || transcribe.IsRetryable(err) {
				t.Errorf("err = %v, want permanent wrapping the splitter error", err)
			}
			assertEmptyTempDir(t, tmp)
		})
	}
}

func TestSplitterFailureUnderCanceledContextIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second, ExtractErr: errors.New("signal: killed")}
	cancel()

	_, err := chunkedClient(chunkConfig("http://127.0.0.1:1"), splitter).Transcribe(ctx, bigAudio(t))

	if !transcribe.IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestInvalidChunkingIsPermanent(t *testing.T) {
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second}
	client := transcribe.New(chunkConfig("http://127.0.0.1:1"), transcribe.WithSplitter(splitter), transcribe.WithChunking(2*time.Second, 2*time.Second))

	_, err := client.Transcribe(t.Context(), bigAudio(t))

	if !errors.Is(err, transcribe.ErrInvalidChunking) || transcribe.IsRetryable(err) {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultChunkingSplitsOnTenMinuteBoundaries(t *testing.T) {
	srv := chunkServer(t, nil, nil)
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 25 * time.Minute}

	_, err := transcribe.New(chunkConfig(srv.URL), transcribe.WithSplitter(splitter)).Transcribe(t.Context(), bigAudio(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	var starts []string
	for _, e := range splitter.Extracts() {
		starts = append(starts, fmt.Sprint(e.Start))
	}
	if got := strings.Join(starts, " "); got != "0s 9m55s 19m50s" {
		t.Errorf("chunk starts = %s", got)
	}
}
