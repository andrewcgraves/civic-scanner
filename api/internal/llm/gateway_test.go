package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
)

func gatewayConfig(url string) config.LLM {
	return config.LLM{
		APIKey:       "test-key",
		BaseURL:      url + "/v1",
		RoutineModel: "vendor/cheap",
		StrongModel:  "vendor/strong",
		Timeout:      5 * time.Second,
	}
}

func TestCompleteSendsChatCompletionAndReturnsTextAndUsage(t *testing.T) {
	var got struct {
		path, auth, contentType string
		body                    map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.Method + " " + r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.contentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-1",
			"object": "chat.completion",
			"model": "vendor/cheap",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "A short summary."}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 15, "completion_tokens": 28, "total_tokens": 43}
		}`))
	}))
	defer srv.Close()

	client := llm.New(gatewayConfig(srv.URL))
	resp, err := client.Complete(t.Context(), llm.Request{
		Tier:   llm.TierRoutine,
		System: "You summarize agendas.",
		User:   "Agenda text",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	want := llm.Response{
		Text:         "A short summary.",
		Model:        "vendor/cheap",
		FinishReason: "stop",
		Usage:        llm.Usage{InputTokens: 15, OutputTokens: 28},
	}
	if resp != want {
		t.Errorf("response = %+v, want %+v", resp, want)
	}
	if got.path != "POST /v1/chat/completions" {
		t.Errorf("request = %q, want POST /v1/chat/completions", got.path)
	}
	if got.auth != "Bearer test-key" {
		t.Errorf("Authorization = %q", got.auth)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q", got.contentType)
	}
	body, _ := json.Marshal(got.body)
	wantBody := `{"messages":[{"content":"You summarize agendas.","role":"system"},{"content":"Agenda text","role":"user"}],"model":"vendor/cheap"}`
	if string(body) != wantBody {
		t.Errorf("body = %s, want %s", body, wantBody)
	}
}

func recordingServer(t *testing.T, bodies *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		*bodies = append(*bodies, body)
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCompleteMapsTierToConfiguredModel(t *testing.T) {
	var bodies []map[string]any
	srv := recordingServer(t, &bodies)
	client := llm.New(gatewayConfig(srv.URL))

	for _, tier := range []llm.Tier{llm.TierRoutine, llm.TierStrong} {
		if _, err := client.Complete(t.Context(), llm.Request{Tier: tier, System: "s", User: "u"}); err != nil {
			t.Fatalf("Complete(%s): %v", tier, err)
		}
	}

	if len(bodies) != 2 || bodies[0]["model"] != "vendor/cheap" || bodies[1]["model"] != "vendor/strong" {
		t.Errorf("models sent = %v", bodies)
	}
}

func TestCompleteSendsMaxTokensOnlyWhenSet(t *testing.T) {
	var bodies []map[string]any
	srv := recordingServer(t, &bodies)
	client := llm.New(gatewayConfig(srv.URL))

	for _, limit := range []int{500, 0} {
		if _, err := client.Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u", MaxOutputTokens: limit}); err != nil {
			t.Fatalf("Complete(MaxOutputTokens=%d): %v", limit, err)
		}
	}

	if got := bodies[0]["max_tokens"]; got != float64(500) {
		t.Errorf("max_tokens = %v, want 500", got)
	}
	if _, sent := bodies[1]["max_tokens"]; sent {
		t.Errorf("max_tokens sent without a limit: %v", bodies[1])
	}
}

func replyServer(t *testing.T, content, finishReason string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "vendor/cheap",
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": content},
				"finish_reason": finishReason,
			}},
			"usage": map[string]int{"prompt_tokens": 900, "completion_tokens": 500},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCompleteTruncatedReplyIsPermanentAndKeepsUsage(t *testing.T) {
	srv := replyServer(t, "The meeting began with", "length")

	resp, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u", MaxOutputTokens: 500})

	if !errors.Is(err, llm.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if llm.IsRetryable(err) {
		t.Error("truncated reply reported as retryable")
	}
	if resp.FinishReason != "length" || resp.Usage.OutputTokens != 500 {
		t.Errorf("response = %+v, want finish reason and usage kept", resp)
	}
}

func TestCompleteFilteredReplyIsPermanent(t *testing.T) {
	srv := replyServer(t, "", "content_filter")

	_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if !errors.Is(err, llm.ErrContentFiltered) {
		t.Fatalf("err = %v, want ErrContentFiltered", err)
	}
	if llm.IsRetryable(err) {
		t.Error("filtered reply reported as retryable")
	}
}

func TestCompleteEmptyReplyIsRetryable(t *testing.T) {
	for _, finishReason := range []string{"stop", ""} {
		t.Run("finish_reason="+finishReason, func(t *testing.T) {
			srv := replyServer(t, "", finishReason)

			_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

			if err == nil || !llm.IsRetryable(err) {
				t.Errorf("err = %v, want a retryable error", err)
			}
		})
	}
}

func TestCompleteOmitsEmptySystemPrompt(t *testing.T) {
	var bodies []map[string]any
	srv := recordingServer(t, &bodies)

	if _, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, _ := json.Marshal(bodies[0]["messages"])
	if string(msgs) != `[{"content":"u","role":"user"}]` {
		t.Errorf("messages = %s", msgs)
	}
}

func TestCompleteClassifiesGatewayStatus(t *testing.T) {
	cases := []struct {
		status    int
		retryable bool
		account   bool
	}{
		{http.StatusBadRequest, false, false},
		{http.StatusUnauthorized, false, true},
		{http.StatusPaymentRequired, false, true},
		{http.StatusForbidden, false, true},
		{http.StatusNotFound, false, true},
		{http.StatusRequestEntityTooLarge, false, false},
		{http.StatusUnprocessableEntity, false, false},
		{http.StatusRequestTimeout, true, false},
		{http.StatusTooManyRequests, true, false},
		{http.StatusInternalServerError, true, false},
		{http.StatusBadGateway, true, false},
		{http.StatusServiceUnavailable, true, false},
		{http.StatusGatewayTimeout, true, false},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"gateway says no","type":"invalid_request_error","code":"some_code"}}`))
			}))
			defer srv.Close()

			_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

			var llmErr *llm.Error
			if !errors.As(err, &llmErr) {
				t.Fatalf("err = %v, want *llm.Error", err)
			}
			if llmErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", llmErr.StatusCode, tc.status)
			}
			if llm.IsRetryable(err) != tc.retryable {
				t.Errorf("IsRetryable = %v, want %v", llm.IsRetryable(err), tc.retryable)
			}
			if llm.IsAccountFailure(err) != tc.account {
				t.Errorf("IsAccountFailure = %v, want %v", llm.IsAccountFailure(err), tc.account)
			}
			if llmErr.Provider != config.LLMGateway || llmErr.Model != "vendor/cheap" || llmErr.Type != "invalid_request_error" || llmErr.Code != "some_code" {
				t.Errorf("error fields = %q %q %q %q, want provider, model, type and code", llmErr.Provider, llmErr.Model, llmErr.Type, llmErr.Code)
			}
			if !strings.Contains(err.Error(), "gateway says no") || !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("error %q lacks status or gateway message", err)
			}
		})
	}
}

func TestCompleteReadsGatewayErrorCodeOfAnyShape(t *testing.T) {
	for body, want := range map[string]string{
		`{"error":{"message":"m","code":403}}`:                               "403",
		`{"error":{"message":"m","code":null}}`:                              "",
		`{"error":{"message":"m"}}`:                                          "",
		`{"error":{"message":"m","code":"` + strings.Repeat("x", 65) + `"}}`: "unrecognized",
		`{"error":{"message":"m","code":"line\nbreak"}}`:                     "unrecognized",
		`not json`: "",
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

			var llmErr *llm.Error
			if !errors.As(err, &llmErr) || llmErr.Code != want {
				t.Errorf("err = %v, code = %q, want %q", err, llmErr.Code, want)
			}
		})
	}
}

func TestCompleteReportsRetryAfterOnRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	var llmErr *llm.Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("err = %v, want *llm.Error", err)
	}
	if llmErr.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %s, want 7s", llmErr.RetryAfter)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error %q lacks status", err)
	}
}

func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func TestCompleteNetworkFailureIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := llm.New(gatewayConfig(url)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if err == nil || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
}

func TestCompleteTimesOutAsRetryable(t *testing.T) {
	cfg := gatewayConfig(hangingServer(t).URL)
	cfg.Timeout = 50 * time.Millisecond

	start := time.Now()
	_, err := llm.New(cfg).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if err == nil || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("returned after %s, want about the 50ms timeout", elapsed)
	}
}

func TestCompleteStopsWhenContextIsCanceled(t *testing.T) {
	cfg := gatewayConfig(hangingServer(t).URL)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err := llm.New(cfg).Complete(ctx, llm.Request{Tier: llm.TierRoutine, User: "u"})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !llm.IsRetryable(err) {
		t.Error("canceled call reported as permanent")
	}
}

func TestCompleteUnusableSuccessBodyIsRetryable(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":  `{"choices": [`,
		"no choices": `{"model":"m","choices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

			if err == nil || !llm.IsRetryable(err) {
				t.Errorf("err = %v, want a retryable error", err)
			}
		})
	}
}

func TestCompleteTruncatedResponseIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte(`{"choices":`))
	}))
	defer srv.Close()

	_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if err == nil || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
}

func TestCompleteOversizedResponseIsPermanent(t *testing.T) {
	srv := replyServer(t, strings.Repeat("a", 9<<20), "stop")

	_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if !errors.Is(err, llm.ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if llm.IsRetryable(err) {
		t.Error("oversized response reported as retryable")
	}
}

func TestCompleteInvalidBaseURLIsPermanent(t *testing.T) {
	cfg := gatewayConfig("")
	cfg.BaseURL = "http://bad host/v1"

	_, err := llm.New(cfg).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if err == nil || llm.IsRetryable(err) {
		t.Errorf("err = %v, want a permanent error", err)
	}
}

func TestCompleteWithoutAPIKeyReturnsNotConfigured(t *testing.T) {
	var bodies []map[string]any
	srv := recordingServer(t, &bodies)
	cfg := gatewayConfig(srv.URL)
	cfg.APIKey = ""

	_, err := llm.New(cfg).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if !errors.Is(err, llm.ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
	if llm.IsRetryable(err) {
		t.Error("not configured reported as retryable")
	}
	if len(bodies) != 0 {
		t.Errorf("gateway called %d times", len(bodies))
	}
}

func TestCompleteRejectsUnknownTierWithoutCallingGateway(t *testing.T) {
	var bodies []map[string]any
	srv := recordingServer(t, &bodies)

	_, err := llm.New(gatewayConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: "premium", User: "u"})

	if !errors.Is(err, llm.ErrUnknownTier) {
		t.Errorf("err = %v, want ErrUnknownTier", err)
	}
	if llm.IsRetryable(err) {
		t.Error("unknown tier reported as retryable")
	}
	if len(bodies) != 0 {
		t.Errorf("gateway called %d times", len(bodies))
	}
}
