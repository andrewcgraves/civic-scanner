package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	AnthropicThinkingHeadroom = 4096
	AnthropicDefaultMaxTokens = 16000
	AnthropicMaxRetries       = 0
)

type anthropicClient struct {
	cfg    config.LLM
	client anthropic.Client
}

func newAnthropic(cfg config.LLM) Client {
	return &anthropicClient{
		cfg: cfg,
		client: anthropic.NewClient(
			option.WithoutEnvironmentDefaults(),
			option.WithAPIKey(cfg.APIKey),
			option.WithBaseURL(cfg.BaseURL),
			option.WithHTTPClient(&http.Client{}),
			option.WithMaxRetries(AnthropicMaxRetries),
			option.WithRequestTimeout(cfg.Timeout),
		),
	}
}

func AnthropicMaxTokens(requested int) int64 {
	if requested <= 0 {
		return AnthropicDefaultMaxTokens
	}
	return int64(requested) + AnthropicThinkingHeadroom
}

func (a *anthropicClient) Complete(ctx context.Context, req Request) (Response, error) {
	if a.cfg.APIKey == "" {
		return Response{}, ErrNotConfigured
	}
	model, err := modelFor(a.cfg, req.Tier)
	if err != nil {
		return Response{}, err
	}
	params := anthropic.MessageNewParams{
		Model:        model,
		MaxTokens:    AnthropicMaxTokens(req.MaxOutputTokens),
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(req.User))},
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(a.cfg.Effort)},
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, anthropicError(err)
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	resp := Response{
		Text:         text.String(),
		Model:        msg.Model,
		FinishReason: finishReason(msg.StopReason),
		Usage: Usage{
			InputTokens:  int(msg.Usage.InputTokens + msg.Usage.CacheCreationInputTokens + msg.Usage.CacheReadInputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
		},
	}
	switch msg.StopReason {
	case anthropic.StopReasonRefusal:
		return resp, fmt.Errorf("%w: the model declined the request (category %q)", ErrContentFiltered, msg.StopDetails.Category)
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence, anthropic.StopReasonMaxTokens, anthropic.StopReasonModelContextWindowExceeded:
		return resp, CheckReply(resp)
	default:
		return resp, &Error{Retryable: true, Err: fmt.Errorf("unexpected stop_reason %q", msg.StopReason)}
	}
}

func finishReason(reason anthropic.StopReason) string {
	switch reason {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
		return "stop"
	case anthropic.StopReasonMaxTokens, anthropic.StopReasonModelContextWindowExceeded:
		return "length"
	case anthropic.StopReasonRefusal:
		return "content_filter"
	default:
		return string(reason)
	}
}

func anthropicError(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return &Error{Retryable: true, Err: err}
	}
	message := apiErr.Message
	if message == "" {
		message = http.StatusText(apiErr.StatusCode)
	}
	e := &Error{
		StatusCode: apiErr.StatusCode,
		Retryable:  retryableStatus(apiErr.StatusCode),
		Err:        fmt.Errorf("anthropic returned %d %s: %s (request %s)", apiErr.StatusCode, apiErr.Type(), message, apiErr.RequestID),
	}
	if apiErr.Response != nil {
		e.RetryAfter = retryAfter(apiErr.Response.Header)
	}
	return e
}
