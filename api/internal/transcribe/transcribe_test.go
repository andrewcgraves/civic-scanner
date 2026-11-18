package transcribe_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

const verboseReply = `{
  "task": "transcribe",
  "language": "English",
  "duration": 12.5,
  "text": " Call to order. Roll call.",
  "segments": [
    {"id": 0, "seek": 0, "start": 0.0, "end": 4.25, "text": " Call to order."},
    {"id": 1, "seek": 0, "start": 4.25, "end": 12.5, "text": " Roll call."}
  ]
}`

func audioFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meeting.mp3")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(url string) config.Transcription {
	return config.Transcription{
		APIKey:         "gsk-test",
		BaseURL:        url,
		Model:          "whisper-large-v3-turbo",
		Timeout:        5 * time.Second,
		MaxUploadBytes: 1 << 20,
	}
}

func TestTranscribeSendsMultipartAndParsesSegments(t *testing.T) {
	var gotPath, gotAuth, gotFile, gotLength string
	fields := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotLength = r.Header.Get("Content-Length")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
			return
		}
		b, _ := io.ReadAll(f)
		gotFile = string(b)
		_, _ = io.WriteString(w, verboseReply)
	}))
	defer srv.Close()

	got, err := transcribe.New(testConfig(srv.URL+"/openai/v1")).Transcribe(t.Context(), audioFile(t, "audio-bytes"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if gotPath != "/openai/v1/audio/transcriptions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer gsk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotFile != "audio-bytes" {
		t.Errorf("uploaded file = %q", gotFile)
	}
	if gotLength == "" {
		t.Error("request has no Content-Length, so the upload was not sized up front")
	}
	if fields["model"][0] != "whisper-large-v3-turbo" || fields["response_format"][0] != "verbose_json" || fields["timestamp_granularities[]"][0] != "segment" {
		t.Errorf("fields = %v", fields)
	}
	want := transcribe.Transcript{
		Text:     "Call to order. Roll call.",
		Duration: 12500 * time.Millisecond,
		Segments: []transcribe.Segment{
			{Start: 0, End: 4250 * time.Millisecond, Text: "Call to order."},
			{Start: 4250 * time.Millisecond, End: 12500 * time.Millisecond, Text: "Roll call."},
		},
	}
	assertTranscript(t, got, want)
}

func assertTranscript(t *testing.T, got, want transcribe.Transcript) {
	t.Helper()
	if got.Text != want.Text || got.Duration != want.Duration || len(got.Segments) != len(want.Segments) {
		t.Fatalf("transcript = %+v, want %+v", got, want)
	}
	for i := range want.Segments {
		if got.Segments[i] != want.Segments[i] {
			t.Errorf("segment %d = %+v, want %+v", i, got.Segments[i], want.Segments[i])
		}
	}
}

func TestTranscribeWithoutSegmentsUsesTheText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"duration": 3, "text": " Silence.", "segments": []}`)
	}))
	defer srv.Close()

	got, err := transcribe.New(testConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "a"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	assertTranscript(t, got, transcribe.Transcript{Text: "Silence.", Duration: 3 * time.Second})
}

func TestTranscribeNotConfiguredMakesNoRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.APIKey = ""

	_, err := transcribe.New(cfg).Transcribe(t.Context(), audioFile(t, "a"))

	if !errors.Is(err, transcribe.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if transcribe.IsRetryable(err) {
		t.Error("not configured must be permanent")
	}
	if calls.Load() != 0 {
		t.Errorf("made %d requests", calls.Load())
	}
}

func TestTranscribeStatusErrors(t *testing.T) {
	cases := []struct {
		status    int
		retryable bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusRequestEntityTooLarge, false},
		{http.StatusUnprocessableEntity, false},
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":{"message":"provider said no"}}`)
			}))
			defer srv.Close()

			_, err := transcribe.New(testConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "a"))

			if err == nil {
				t.Fatal("expected an error")
			}
			if transcribe.IsRetryable(err) != tc.retryable {
				t.Errorf("IsRetryable = %v, want %v", transcribe.IsRetryable(err), tc.retryable)
			}
			var terr *transcribe.Error
			if !errors.As(err, &terr) || terr.StatusCode != tc.status {
				t.Fatalf("err = %v, want *Error with status %d", err, tc.status)
			}
			if !strings.Contains(err.Error(), "provider said no") {
				t.Errorf("message lost: %v", err)
			}
			if tc.status == http.StatusTooManyRequests && terr.RetryAfter != 7*time.Second {
				t.Errorf("RetryAfter = %v", terr.RetryAfter)
			}
		})
	}
}

func TestTranscribeStatusErrorWithoutJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>bad gateway</html>")
	}))
	defer srv.Close()

	_, err := transcribe.New(testConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "a"))

	if !transcribe.IsRetryable(err) || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Errorf("err = %v", err)
	}
}

func TestTranscribeNetworkFailureIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := transcribe.New(testConfig(url)).Transcribe(t.Context(), audioFile(t, "a"))

	if !transcribe.IsRetryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}

func TestTranscribeTimeoutIsRetryable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	cfg := testConfig(srv.URL)
	cfg.Timeout = 50 * time.Millisecond

	_, err := transcribe.New(cfg).Transcribe(t.Context(), audioFile(t, "a"))

	if !transcribe.IsRetryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}

func TestTranscribeCanceledContextIsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := transcribe.New(testConfig("http://127.0.0.1:1")).Transcribe(ctx, audioFile(t, "a"))

	if !transcribe.IsRetryable(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestTranscribeBadReplyIsRetryable(t *testing.T) {
	for name, body := range map[string]string{"not json": "oops", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()

			_, err := transcribe.New(testConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "a"))

			if !transcribe.IsRetryable(err) {
				t.Errorf("err = %v, want retryable", err)
			}
		})
	}
}

func TestTranscribeOversizedReplyIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 33<<20))
	}))
	defer srv.Close()

	_, err := transcribe.New(testConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "a"))

	if !errors.Is(err, transcribe.ErrResponseTooLarge) || transcribe.IsRetryable(err) {
		t.Errorf("err = %v", err)
	}
}

func TestTranscribeMissingFileIsPermanent(t *testing.T) {
	_, err := transcribe.New(testConfig("http://127.0.0.1:1")).Transcribe(t.Context(), filepath.Join(t.TempDir(), "missing.mp3"))

	if !errors.Is(err, os.ErrNotExist) || transcribe.IsRetryable(err) {
		t.Errorf("err = %v", err)
	}
}
