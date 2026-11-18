package transcribe_test

import (
	"encoding/base64"
	"encoding/json"
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
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe/transcribetest"
)

const plainReply = `{
  "text": "Welcome to the meeting. Let's get started.",
  "segments": [
    {"text": "Welcome to the meeting.", "startSecond": 0, "endSecond": 2.5},
    {"text": "Let's get started.", "startSecond": 2.5, "endSecond": 4.2}
  ],
  "language": "en",
  "durationInSeconds": 4.2,
  "warnings": []
}`

func gatewayConfig(origin string) config.Transcription {
	return config.Transcription{
		Provider:          config.TranscriptionGateway,
		APIKey:            "vck-test",
		BaseURL:           origin,
		Model:             "elevenlabs/scribe-v2",
		Timeout:           5 * time.Second,
		DirectUploadBytes: 1 << 20,
		MaxChunkBytes:     1 << 20,
	}
}

func gatewayServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
}

func TestGatewaySendsBase64JSONAndParsesSegments(t *testing.T) {
	var gotPath, gotMethod, gotType string
	var gotHeaders http.Header
	var gotBody map[string]any
	var contentLength, bodyLength int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotType, gotHeaders = r.URL.Path, r.Method, r.Header.Get("Content-Type"), r.Header
		contentLength = r.ContentLength
		raw, _ := io.ReadAll(r.Body)
		bodyLength = int64(len(raw))
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		_, _ = io.WriteString(w, plainReply)
	}))
	defer srv.Close()
	audio := "audio-bytes-that-are-not-a-multiple-of-three"

	got, err := transcribe.New(gatewayConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, audio))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/v4/ai/transcription-model" || gotType != "application/json" {
		t.Errorf("request = %s %s %s", gotMethod, gotPath, gotType)
	}
	wantHeaders := map[string]string{
		"Authorization":                                "Bearer vck-test",
		"Ai-Gateway-Protocol-Version":                  "0.0.1",
		"Ai-Transcription-Model-Specification-Version": "4",
		"Ai-Model-Id":                                  "elevenlabs/scribe-v2",
	}
	for k, v := range wantHeaders {
		if gotHeaders.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, gotHeaders.Get(k), v)
		}
	}
	if gotBody["audio"] != base64.StdEncoding.EncodeToString([]byte(audio)) || gotBody["mediaType"] != "audio/mpeg" {
		t.Errorf("body = %v", gotBody)
	}
	if _, ok := gotBody["providerOptions"]; ok {
		t.Errorf("providerOptions sent without configuration")
	}
	if contentLength != bodyLength {
		t.Errorf("Content-Length = %d, body = %d", contentLength, bodyLength)
	}
	assertTranscript(t, got, transcribe.Transcript{
		Text:     "Welcome to the meeting. Let's get started.",
		Duration: 4200 * time.Millisecond,
		Segments: []transcribe.Segment{
			{End: 2500 * time.Millisecond, Text: "Welcome to the meeting."},
			{Start: 2500 * time.Millisecond, End: 4200 * time.Millisecond, Text: "Let's get started."},
		},
	})
}

func TestGatewayPassesProviderOptionsThrough(t *testing.T) {
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, plainReply)
	}))
	defer srv.Close()
	cfg := gatewayConfig(srv.URL)
	cfg.ProviderOptions = `{"azure":{"diarization":{"enabled":true}}}`

	if _, err := transcribe.New(cfg).Transcribe(t.Context(), audioFile(t, "x")); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if string(gotBody["providerOptions"]) != cfg.ProviderOptions {
		t.Errorf("providerOptions = %s", gotBody["providerOptions"])
	}
}

const elevenLabsWords = `{
  "text": "Good evening. Roll call please. Present. [laughs]",
  "segments": [],
  "language": "en",
  "durationInSeconds": 12,
  "providerMetadata": {"elevenlabs": {"words": [
    {"text": "Good", "type": "word", "start": 0.0, "end": 0.4, "speaker_id": "speaker_0"},
    {"text": " ", "type": "spacing", "start": 0.4, "end": 0.45, "speaker_id": "speaker_0"},
    {"text": "evening.", "type": "word", "start": 0.45, "end": 1.0, "speaker_id": "speaker_0"},
    {"text": " ", "type": "spacing", "start": 1.0, "end": 1.1, "speaker_id": "speaker_0"},
    {"text": "Roll", "type": "word", "start": 1.1, "end": 1.4, "speaker_id": "speaker_0"},
    {"text": " ", "type": "spacing", "start": 1.4, "end": 1.45, "speaker_id": "speaker_0"},
    {"text": "call", "type": "word", "start": 1.45, "end": 1.8, "speaker_id": "speaker_0"},
    {"text": " ", "type": "spacing", "start": 1.8, "end": 1.85, "speaker_id": "speaker_0"},
    {"text": "please.", "type": "word", "start": 1.85, "end": 2.4, "speaker_id": "speaker_0"},
    {"text": " ", "type": "spacing", "start": 2.4, "end": 3.0, "speaker_id": "speaker_1"},
    {"text": "Present.", "type": "word", "start": 3.0, "end": 3.5, "speaker_id": "speaker_1"},
    {"text": " ", "type": "spacing", "start": 3.5, "end": 8.0, "speaker_id": "speaker_1"},
    {"text": "[laughs]", "type": "audio_event", "start": 8.0, "end": 9.0, "speaker_id": "speaker_1"}
  ]}}
}`

func TestGatewayBuildsSegmentsFromElevenLabsWords(t *testing.T) {
	srv := gatewayServer(t, elevenLabsWords)
	defer srv.Close()

	got, err := transcribe.New(gatewayConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "x"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	ms := time.Millisecond
	assertTranscript(t, got, transcribe.Transcript{
		Text:     "Good evening. Roll call please. Present. [laughs]",
		Duration: 12 * time.Second,
		Segments: []transcribe.Segment{
			{Start: 0, End: 1000 * ms, Text: "Good evening.", Speaker: "c0/speaker_0"},
			{Start: 1100 * ms, End: 2400 * ms, Text: "Roll call please.", Speaker: "c0/speaker_0"},
			{Start: 3000 * ms, End: 3500 * ms, Text: "Present.", Speaker: "c0/speaker_1"},
			{Start: 8000 * ms, End: 9000 * ms, Text: "[laughs]", Speaker: "c0/speaker_1"},
		},
	})
}

func TestGatewaySplitsWordsOnSpeakerChangeAndLongPauses(t *testing.T) {
	srv := gatewayServer(t, `{"text":"a b c d","providerMetadata":{"elevenlabs":{"words":[
    {"text":"a","type":"word","start":0,"end":1,"speaker_id":"speaker_0"},
    {"text":" ","type":"spacing","start":1,"end":1.1,"speaker_id":"speaker_0"},
    {"text":"b","type":"word","start":1.1,"end":2,"speaker_id":"speaker_1"},
    {"text":" ","type":"spacing","start":2,"end":6,"speaker_id":"speaker_1"},
    {"text":"c","type":"word","start":6,"end":7,"speaker_id":"speaker_1"},
    {"text":" ","type":"spacing","start":7,"end":7.1,"speaker_id":"speaker_1"},
    {"text":"d","type":"word","start":7.1,"end":8,"speaker_id":"speaker_1"}
  ]}}}`)
	defer srv.Close()

	got, err := transcribe.New(gatewayConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "x"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	var texts []string
	for _, s := range got.Segments {
		texts = append(texts, s.Speaker+"="+s.Text)
	}
	if want := "c0/speaker_0=a c0/speaker_1=b c0/speaker_1=c d"; strings.Join(texts, " ") != want {
		t.Errorf("segments = %q, want %q", strings.Join(texts, " "), want)
	}
}

func TestGatewayLabelsExistingSegmentsFromWordSpeakers(t *testing.T) {
	srv := gatewayServer(t, `{"text":"hi there","segments":[
    {"text":"hi","startSecond":0,"endSecond":1},
    {"text":"there","startSecond":1,"endSecond":2}
  ],"providerMetadata":{"elevenlabs":{"words":[
    {"text":"hi","type":"word","start":0,"end":1,"speaker_id":"speaker_3"},
    {"text":"there","type":"word","start":1,"end":2,"speaker_id":"speaker_4"}
  ]}}}`)
	defer srv.Close()

	got, err := transcribe.New(gatewayConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "x"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if len(got.Segments) != 2 || got.Segments[0].Speaker != "c0/speaker_3" || got.Segments[1].Speaker != "c0/speaker_4" {
		t.Errorf("segments = %+v", got.Segments)
	}
}

func TestGatewayUsesAzurePhrasesWithSpeakers(t *testing.T) {
	srv := gatewayServer(t, `{"text":"Call to order. Here.","segments":[
    {"text":"Call to order.","startSecond":0,"endSecond":2},
    {"text":"Here.","startSecond":2.5,"endSecond":3}
  ],"durationInSeconds":3,"providerMetadata":{"azure":{"phrases":[
    {"text":"Call to order.","offsetMilliseconds":0,"durationMilliseconds":2000,"speaker":1},
    {"text":"Here.","offsetMilliseconds":2500,"durationMilliseconds":500,"speaker":2}
  ]}}}`)
	defer srv.Close()

	got, err := transcribe.New(gatewayConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "x"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	assertTranscript(t, got, transcribe.Transcript{
		Text:     "Call to order. Here.",
		Duration: 3 * time.Second,
		Segments: []transcribe.Segment{
			{End: 2 * time.Second, Text: "Call to order.", Speaker: "c0/1"},
			{Start: 2500 * time.Millisecond, End: 3 * time.Second, Text: "Here.", Speaker: "c0/2"},
		},
	})
}

func TestGatewayWithoutSegmentsOrWordsUsesTheText(t *testing.T) {
	srv := gatewayServer(t, `{"text":" Just text. ","segments":[],"durationInSeconds":2}`)
	defer srv.Close()

	got, err := transcribe.New(gatewayConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "x"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	assertTranscript(t, got, transcribe.Transcript{Text: "Just text.", Duration: 2 * time.Second, Segments: []transcribe.Segment{}})
}

func TestGatewayNotConfiguredMakesNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	cfg := gatewayConfig(srv.URL)
	cfg.APIKey = ""

	_, err := transcribe.New(cfg).Transcribe(t.Context(), audioFile(t, "x"))

	if !errors.Is(err, transcribe.ErrNotConfigured) || transcribe.IsRetryable(err) || hits.Load() != 0 {
		t.Errorf("err = %v, hits = %d", err, hits.Load())
	}
}

func TestGatewayStatusErrors(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		retryable bool
	}{
		{http.StatusBadRequest, `{"error":{"message":"unsupported option"}}`, false},
		{http.StatusUnauthorized, `{"error":{"message":"bad key"}}`, false},
		{http.StatusPaymentRequired, `{"error":{"message":"out of credits"}}`, true},
		{http.StatusRequestEntityTooLarge, `{"message":"too big"}`, false},
		{http.StatusRequestTimeout, ``, true},
		{http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, true},
		{http.StatusInternalServerError, `oops`, true},
		{http.StatusBadGateway, ``, true},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := transcribe.New(gatewayConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "x"))

			var e *transcribe.Error
			if !errors.As(err, &e) || e.StatusCode != tc.status {
				t.Fatalf("err = %v, want *transcribe.Error with status %d", err, tc.status)
			}
			if transcribe.IsRetryable(err) != tc.retryable {
				t.Errorf("retryable = %v, want %v", transcribe.IsRetryable(err), tc.retryable)
			}
			if tc.retryable && tc.status == http.StatusTooManyRequests && e.RetryAfter != 7*time.Second {
				t.Errorf("RetryAfter = %v", e.RetryAfter)
			}
		})
	}
}

func TestGatewayStatusErrorCarriesTheMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"unsupported option"}}`)
	}))
	defer srv.Close()

	_, err := transcribe.New(gatewayConfig(srv.URL)).Transcribe(t.Context(), audioFile(t, "x"))

	if err == nil || !strings.Contains(err.Error(), "unsupported option") || !strings.Contains(err.Error(), "gateway") {
		t.Errorf("err = %v", err)
	}
}

func TestGatewayTransportFailuresAreRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := transcribe.New(gatewayConfig(url)).Transcribe(t.Context(), audioFile(t, "x"))

	if !transcribe.IsRetryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}

func TestGatewayTimeoutIsRetryable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	cfg := gatewayConfig(srv.URL)
	cfg.Timeout = 50 * time.Millisecond

	_, err := transcribe.New(cfg).Transcribe(t.Context(), audioFile(t, "x"))

	if !transcribe.IsRetryable(err) {
		t.Errorf("err = %v, want retryable", err)
	}
}

func TestGatewayBadReplyIsRetryableAndOversizedReplyIsNot(t *testing.T) {
	bad := gatewayServer(t, `not json`)
	defer bad.Close()
	_, err := transcribe.New(gatewayConfig(bad.URL)).Transcribe(t.Context(), audioFile(t, "x"))
	if !transcribe.IsRetryable(err) {
		t.Errorf("bad reply err = %v, want retryable", err)
	}

	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"text":"`)
		chunk := strings.Repeat("a", 1<<20)
		for range 33 {
			_, _ = io.WriteString(w, chunk)
		}
		_, _ = io.WriteString(w, `"}`)
	}))
	defer huge.Close()
	_, err = transcribe.New(gatewayConfig(huge.URL)).Transcribe(t.Context(), audioFile(t, "x"))
	if !errors.Is(err, transcribe.ErrResponseTooLarge) || transcribe.IsRetryable(err) {
		t.Errorf("oversized err = %v", err)
	}
}

func TestGatewayMissingFileIsPermanent(t *testing.T) {
	_, err := transcribe.New(gatewayConfig("http://127.0.0.1:1")).Transcribe(t.Context(), filepath.Join(t.TempDir(), "missing.mp3"))

	if err == nil || transcribe.IsRetryable(err) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v", err)
	}
}

func gatewayChunkServer(t *testing.T, byChunk map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Audio string `json:"audio"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := base64.StdEncoding.DecodeString(body.Audio)
		_, _ = io.WriteString(w, byChunk[string(raw)])
	}))
}

func speakerReply(word string, speaker string) string {
	return `{"text":"` + word + `","durationInSeconds":10,"providerMetadata":{"elevenlabs":{"words":[` +
		`{"text":"` + word + `","type":"word","start":1,"end":3,"speaker_id":"` + speaker + `"}]}}}`
}

func gatewayChunkedClient(cfg config.Transcription, splitter transcribe.Splitter) transcribe.Client {
	cfg.DirectUploadBytes, cfg.MaxChunkBytes = 64, 64
	return transcribe.New(cfg, transcribe.WithSplitter(splitter), transcribe.WithChunking(10*time.Second, 2*time.Second))
}

func TestGatewayChunkSpeakersAreScopedToTheirChunk(t *testing.T) {
	isolatedTempDir(t)
	srv := gatewayChunkServer(t, map[string]string{
		"chunk@0s": speakerReply("one", "speaker_0"),
		"chunk@8s": speakerReply("two", "speaker_0"),
	})
	defer srv.Close()
	splitter := &transcribetest.FakeSplitter{Total: 12 * time.Second}

	got, err := gatewayChunkedClient(gatewayConfig(srv.URL), splitter).Transcribe(t.Context(), bigAudio(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if len(got.Segments) != 2 {
		t.Fatalf("segments = %+v", got.Segments)
	}
	first, second := got.Segments[0], got.Segments[1]
	if first.Speaker != "c0/speaker_0" || second.Speaker != "c1/speaker_0" {
		t.Errorf("speakers = %q, %q", first.Speaker, second.Speaker)
	}
	if second.Start != 9*time.Second || second.End != 11*time.Second {
		t.Errorf("second segment = %+v", second)
	}
}

func TestGatewayInvalidProviderOptionsArePermanent(t *testing.T) {
	cfg := gatewayConfig("http://127.0.0.1:1")
	cfg.ProviderOptions = "diarize"

	_, err := transcribe.New(cfg).Transcribe(t.Context(), audioFile(t, "x"))

	if !errors.Is(err, transcribe.ErrInvalidProviderOptions) || transcribe.IsRetryable(err) {
		t.Errorf("err = %v", err)
	}
}

func TestGatewayAlwaysReencodesLargeAndUnknownFormats(t *testing.T) {
	small := audioFile(t, "tiny")
	wav := filepath.Join(t.TempDir(), "meeting.wav")
	if err := os.WriteFile(wav, []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		path   string
		direct bool
	}{
		"small mp3": {small, true},
		"small wav": {wav, false},
		"large mp3": {bigAudio(t), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolatedTempDir(t)
			srv := gatewayChunkServer(t, map[string]string{"tiny": plainReply, "chunk@0s": plainReply})
			defer srv.Close()
			splitter := &transcribetest.FakeSplitter{Total: 5 * time.Second}

			_, err := gatewayChunkedClient(gatewayConfig(srv.URL), splitter).Transcribe(t.Context(), tc.path)
			if err != nil {
				t.Fatalf("Transcribe: %v", err)
			}

			if direct := len(splitter.Extracts()) == 0; direct != tc.direct {
				t.Errorf("sent without re-encoding = %v, want %v", direct, tc.direct)
			}
		})
	}
}
