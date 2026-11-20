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
	"sync"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
)

const testAnthropicKey = "test-anthropic-key"

func anthropicConfig(url string) config.LLM {
	return config.LLM{
		Provider:     config.LLMAnthropic,
		APIKey:       testAnthropicKey,
		BaseURL:      url,
		RoutineModel: "claude-haiku-5-5",
		StrongModel:  "claude-sonnet-5-5",
		Effort:       "low",
		Timeout:      5 * time.Second,
	}
}

type anthropicRequest struct {
	method, path string
	header       http.Header
	body         map[string]any
}

type fakeAnthropic struct {
	*httptest.Server
	mu       sync.Mutex
	requests []anthropicRequest
}

func (f *fakeAnthropic) received() []anthropicRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]anthropicRequest(nil), f.requests...)
}

func anthropicServer(t *testing.T, status int, header http.Header, body string) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := anthropicRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone()}
		if err := json.Unmarshal(raw, &req.body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		for k, vs := range header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.Close)
	return f
}

func messageBody(model, stopReason, text string) string {
	content := []map[string]any{{"type": "thinking", "thinking": "", "signature": "sig"}}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	body, _ := json.Marshal(map[string]any{
		"id":            "msg_1",
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"stop_details":  nil,
		"usage": map[string]any{
			"input_tokens":                15,
			"cache_creation_input_tokens": 3,
			"cache_read_input_tokens":     2,
			"output_tokens":               28,
			"output_tokens_details":       map[string]any{"thinking_tokens": 9},
		},
	})
	return string(body)
}

func okServer(t *testing.T, stopReason, text string) *fakeAnthropic {
	return anthropicServer(t, http.StatusOK, nil, messageBody("claude-haiku-5-5", stopReason, text))
}

func TestAnthropicSendsMessageAndReturnsTextAndUsage(t *testing.T) {
	srv := okServer(t, "end_turn", "A short summary.")

	resp, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{
		Tier:            llm.TierRoutine,
		System:          "You summarize agendas.",
		User:            "Agenda text",
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	want := llm.Response{
		Text:         "A short summary.",
		Model:        "claude-haiku-5-5",
		FinishReason: "stop",
		Usage:        llm.Usage{InputTokens: 20, OutputTokens: 28},
	}
	if resp != want {
		t.Errorf("resp = %+v, want %+v", resp, want)
	}

	reqs := srv.received()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.method != http.MethodPost || got.path != "/v1/messages" {
		t.Errorf("request = %s %s, want POST /v1/messages", got.method, got.path)
	}
	if got.header.Get("Authorization") != "Bearer "+testAnthropicKey {
		t.Errorf("Authorization = %q, want the configured key", got.header.Get("Authorization"))
	}
	if got.header.Get("Anthropic-Version") == "" {
		t.Error("anthropic-version header missing")
	}

	body := got.body
	if body["model"] != "claude-haiku-5-5" {
		t.Errorf("model = %v, want claude-haiku-5-5", body["model"])
	}
	if body["max_tokens"] != float64(1024+llm.AnthropicThinkingHeadroom) {
		t.Errorf("max_tokens = %v, want %d", body["max_tokens"], 1024+llm.AnthropicThinkingHeadroom)
	}
	system, _ := json.Marshal(body["system"])
	if string(system) != `[{"text":"You summarize agendas.","type":"text"}]` {
		t.Errorf("system = %s", system)
	}
	messages, _ := json.Marshal(body["messages"])
	if string(messages) != `[{"content":[{"text":"Agenda text","type":"text"}],"role":"user"}]` {
		t.Errorf("messages = %s, want one user message and no prefill", messages)
	}
	outputConfig, _ := json.Marshal(body["output_config"])
	if string(outputConfig) != `{"effort":"low"}` {
		t.Errorf("output_config = %s, want effort low", outputConfig)
	}
	for _, key := range []string{"temperature", "top_p", "top_k", "thinking", "stop_sequences", "tools", "tool_choice", "fallbacks", "stream"} {
		if _, ok := body[key]; ok {
			t.Errorf("request sets %q: %v", key, body[key])
		}
	}
}

func TestAnthropicMapsTierToConfiguredModel(t *testing.T) {
	srv := anthropicServer(t, http.StatusOK, nil, messageBody("claude-sonnet-5-5", "end_turn", "ok"))

	resp, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierStrong, User: "u"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := srv.received()[0].body["model"]; got != "claude-sonnet-5-5" {
		t.Errorf("model sent = %v, want claude-sonnet-5-5", got)
	}
	if resp.Model != "claude-sonnet-5-5" {
		t.Errorf("model reported = %q, want the model the API answered with", resp.Model)
	}
}

func TestAnthropicOmitsEmptySystemAndDefaultsMaxTokens(t *testing.T) {
	srv := okServer(t, "end_turn", "ok")

	if _, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	body := srv.received()[0].body
	if _, ok := body["system"]; ok {
		t.Errorf("system sent for an empty system prompt: %v", body["system"])
	}
	if body["max_tokens"] != float64(llm.AnthropicDefaultMaxTokens) {
		t.Errorf("max_tokens = %v, want %d", body["max_tokens"], llm.AnthropicDefaultMaxTokens)
	}
}

func TestAnthropicSendsConfiguredEffort(t *testing.T) {
	srv := okServer(t, "end_turn", "ok")
	cfg := anthropicConfig(srv.URL)
	cfg.Effort = "high"

	if _, err := llm.New(cfg).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	outputConfig, _ := json.Marshal(srv.received()[0].body["output_config"])
	if string(outputConfig) != `{"effort":"high"}` {
		t.Errorf("output_config = %s, want effort high", outputConfig)
	}
}

func TestAnthropicMaxTokensStopIsTruncated(t *testing.T) {
	for _, stopReason := range []string{"max_tokens", "model_context_window_exceeded"} {
		t.Run(stopReason, func(t *testing.T) {
			srv := okServer(t, stopReason, "The meeting began with")

			resp, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u", MaxOutputTokens: 1024})

			if !errors.Is(err, llm.ErrTruncated) || llm.IsRetryable(err) {
				t.Fatalf("err = %v, want permanent ErrTruncated", err)
			}
			if resp.FinishReason != "length" || resp.Usage.OutputTokens != 28 || resp.Model != "claude-haiku-5-5" {
				t.Errorf("resp = %+v, want finish reason length with model and usage kept", resp)
			}
		})
	}
}

func TestAnthropicRefusalIsPermanentContentFiltered(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-5-5",
		"content":[{"type":"text","text":"I can't help with"}],
		"stop_reason":"refusal","stop_sequence":null,
		"stop_details":{"type":"refusal","category":"cyber","explanation":"declined"},
		"usage":{"input_tokens":40,"output_tokens":5}}`
	srv := anthropicServer(t, http.StatusOK, nil, body)

	resp, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if !errors.Is(err, llm.ErrContentFiltered) || llm.IsRetryable(err) {
		t.Fatalf("err = %v, want permanent ErrContentFiltered", err)
	}
	if !strings.Contains(err.Error(), `"cyber"`) {
		t.Errorf("err = %v, want the refusal category", err)
	}
	if resp.FinishReason != "content_filter" || resp.Usage.InputTokens != 40 {
		t.Errorf("resp = %+v, want finish reason content_filter with usage", resp)
	}
}

func TestAnthropicEmptyOrUnexpectedReplyIsRetryable(t *testing.T) {
	for name, tc := range map[string]struct{ stopReason, text string }{
		"only thinking":          {"end_turn", ""},
		"blank text":             {"end_turn", "  \n "},
		"tool use":               {"tool_use", "calling a tool"},
		"pause turn":             {"pause_turn", "partial"},
		"unknown reason":         {"something_new", "text"},
		"stop sequence, no text": {"stop_sequence", ""},
	} {
		t.Run(name, func(t *testing.T) {
			srv := okServer(t, tc.stopReason, tc.text)

			_, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

			if err == nil || !llm.IsRetryable(err) {
				t.Errorf("err = %v, want a retryable error", err)
			}
		})
	}
}

func errorBody(errType, message string) string {
	return `{"type":"error","error":{"type":"` + errType + `","message":"` + message + `"},"request_id":"req_1"}`
}

func TestAnthropicClassifiesStatusWithoutSDKRetries(t *testing.T) {
	for _, tc := range []struct {
		status    int
		errType   string
		retryable bool
	}{
		{http.StatusBadRequest, "invalid_request_error", false},
		{http.StatusUnauthorized, "authentication_error", false},
		{http.StatusForbidden, "permission_error", false},
		{http.StatusNotFound, "not_found_error", false},
		{http.StatusRequestEntityTooLarge, "request_too_large", false},
		{http.StatusPaymentRequired, "billing_error", true},
		{http.StatusRequestTimeout, "timeout_error", true},
		{http.StatusTooManyRequests, "rate_limit_error", true},
		{http.StatusInternalServerError, "api_error", true},
		{http.StatusBadGateway, "api_error", true},
		{http.StatusServiceUnavailable, "api_error", true},
		{529, "overloaded_error", true},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			header := http.Header{"X-Should-Retry": {"true"}, "Request-Id": {"req_1"}}
			srv := anthropicServer(t, tc.status, header, errorBody(tc.errType, "the reason"))

			_, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

			var e *llm.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want *llm.Error", err)
			}
			if e.StatusCode != tc.status || e.Retryable != tc.retryable {
				t.Errorf("status %d retryable %v, want %d %v", e.StatusCode, e.Retryable, tc.status, tc.retryable)
			}
			if !strings.Contains(err.Error(), "the reason") || !strings.Contains(err.Error(), tc.errType) {
				t.Errorf("err = %v, want the API's type and message", err)
			}
			if strings.Contains(err.Error(), testAnthropicKey) {
				t.Errorf("err = %v, contains the key", err)
			}
			if n := len(srv.received()); n != 1 {
				t.Errorf("requests = %d, want 1: the SDK must not retry", n)
			}
		})
	}
}

func TestAnthropicReportsRetryAfterOnRateLimit(t *testing.T) {
	srv := anthropicServer(t, http.StatusTooManyRequests, http.Header{"Retry-After": {"7"}}, errorBody("rate_limit_error", "slow down"))

	_, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	var e *llm.Error
	if !errors.As(err, &e) || e.RetryAfter != 7*time.Second || !e.Retryable {
		t.Errorf("err = %#v, want retryable with RetryAfter 7s", err)
	}
}

func TestAnthropicTimeoutIsRetryableAndNotRetriedBySDK(t *testing.T) {
	var hits int
	var mu sync.Mutex
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	cfg := anthropicConfig(srv.URL)
	cfg.Timeout = 50 * time.Millisecond

	_, err := llm.New(cfg).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if !errors.Is(err, context.DeadlineExceeded) || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable deadline error", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("requests = %d, want 1", hits)
	}
}

func TestAnthropicStopsWhenContextIsCanceled(t *testing.T) {
	srv := hangingServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err := llm.New(anthropicConfig(srv.URL)).Complete(ctx, llm.Request{Tier: llm.TierRoutine, User: "u"})

	if !errors.Is(err, context.Canceled) || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want retryable context.Canceled", err)
	}
}

func TestAnthropicNetworkFailureIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := llm.New(anthropicConfig(url)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if err == nil || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
}

func TestAnthropicUndecodableSuccessIsRetryable(t *testing.T) {
	srv := anthropicServer(t, http.StatusOK, nil, `{"content": [`)

	_, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})

	if err == nil || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
}

func TestAnthropicWithoutKeyOrWithUnknownTierCallsNothing(t *testing.T) {
	srv := okServer(t, "end_turn", "ok")
	noKey := anthropicConfig(srv.URL)
	noKey.APIKey = ""

	_, err := llm.New(noKey).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"})
	if !errors.Is(err, llm.ErrNotConfigured) || llm.IsRetryable(err) {
		t.Errorf("no key: err = %v, want permanent ErrNotConfigured", err)
	}
	_, err = llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: "premium", User: "u"})
	if !errors.Is(err, llm.ErrUnknownTier) || llm.IsRetryable(err) {
		t.Errorf("unknown tier: err = %v, want permanent ErrUnknownTier", err)
	}
	if n := len(srv.received()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

func TestAnthropicIgnoresSDKEnvironmentVariables(t *testing.T) {
	other := okServer(t, "end_turn", "wrong server")
	t.Setenv("ANTHROPIC_BASE_URL", other.URL)
	t.Setenv("ANTHROPIC_API_KEY", "env-anthropic-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "token-from-env")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Injected: yes")
	srv := okServer(t, "end_turn", "ok")

	if _, err := llm.New(anthropicConfig(srv.URL)).Complete(t.Context(), llm.Request{Tier: llm.TierRoutine, User: "u"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if n := len(other.received()); n != 0 {
		t.Errorf("ANTHROPIC_BASE_URL from the environment received %d requests", n)
	}
	got := srv.received()[0].header
	if got.Get("Authorization") != "Bearer "+testAnthropicKey || got.Get("X-Api-Key") != "" || got.Get("X-Injected") != "" {
		t.Errorf("headers = %v, want only the configured key", got)
	}
}
