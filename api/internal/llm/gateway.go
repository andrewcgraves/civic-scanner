package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const maxResponseBytes = 8 << 20

type gateway struct {
	cfg  config.LLM
	http *http.Client
}

func New(cfg config.LLM) Client {
	if cfg.Provider == config.LLMAnthropic {
		return newAnthropic(cfg)
	}
	return &gateway{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type errorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func statusError(resp *http.Response, raw []byte) *Error {
	message := http.StatusText(resp.StatusCode)
	var parsed errorBody
	if json.Unmarshal(raw, &parsed) == nil && parsed.Error.Message != "" {
		message = parsed.Error.Message
	}
	return &Error{
		StatusCode: resp.StatusCode,
		Retryable:  retryableStatus(resp.StatusCode),
		RetryAfter: retryAfter(resp.Header),
		Err:        fmt.Errorf("gateway returned %d: %s", resp.StatusCode, message),
	}
}

func retryAfter(h http.Header) time.Duration {
	if seconds, err := strconv.Atoi(h.Get("Retry-After")); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusPaymentRequired, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= http.StatusInternalServerError
}

func modelFor(cfg config.LLM, tier Tier) (string, error) {
	switch tier {
	case TierRoutine:
		return cfg.RoutineModel, nil
	case TierStrong:
		return cfg.StrongModel, nil
	default:
		return "", fmt.Errorf("%w %q", ErrUnknownTier, tier)
	}
}

func (g *gateway) Complete(ctx context.Context, req Request) (Response, error) {
	if g.cfg.APIKey == "" {
		return Response{}, ErrNotConfigured
	}
	model, err := modelFor(g.cfg, req.Tier)
	if err != nil {
		return Response{}, err
	}
	var messages []chatMessage
	if req.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: req.User})
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, MaxTokens: req.MaxOutputTokens})
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(g.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := g.http.Do(httpReq)
	if err != nil {
		return Response{}, &Error{Retryable: true, Err: err}
	}
	defer func() { _ = httpResp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
	if err != nil {
		return Response{}, &Error{StatusCode: httpResp.StatusCode, Retryable: true, Err: err}
	}
	if httpResp.StatusCode != http.StatusOK {
		return Response{}, statusError(httpResp, raw)
	}
	if len(raw) > maxResponseBytes {
		return Response{}, fmt.Errorf("%w: over %d bytes", ErrResponseTooLarge, maxResponseBytes)
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, &Error{StatusCode: httpResp.StatusCode, Retryable: true, Err: fmt.Errorf("decode gateway response: %w", err)}
	}
	if len(parsed.Choices) == 0 {
		return Response{}, &Error{StatusCode: httpResp.StatusCode, Retryable: true, Err: errors.New("gateway response has no choices")}
	}
	resp := Response{
		Text:         parsed.Choices[0].Message.Content,
		Model:        parsed.Model,
		FinishReason: parsed.Choices[0].FinishReason,
		Usage:        Usage{InputTokens: parsed.Usage.PromptTokens, OutputTokens: parsed.Usage.CompletionTokens},
	}
	return resp, CheckReply(resp)
}
