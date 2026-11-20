package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var (
	ErrNotConfigured    = errors.New("llm: not configured, set AI_GATEWAY_API_KEY, or ANTHROPIC_API_KEY with LLM_PROVIDER=anthropic")
	ErrUnknownTier      = errors.New("llm: unknown tier")
	ErrTruncated        = errors.New("llm: reply truncated at the output token limit")
	ErrContentFiltered  = errors.New("llm: reply blocked by the content filter")
	ErrResponseTooLarge = errors.New("llm: gateway response too large")
	ErrThinkingBudget   = errors.New("thinking used up the output token budget before the reply was written")
)

type Error struct {
	StatusCode int
	Retryable  bool
	Account    bool
	RetryAfter time.Duration
	Provider   string
	Model      string
	Type       string
	Code       string
	Err        error
}

func (e *Error) Error() string { return "llm: " + e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

func IsRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable
}

func IsAccountFailure(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Account
}

func accountStatus(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

func CheckReply(resp Response) error {
	switch {
	case resp.FinishReason == "length":
		return fmt.Errorf("%w after %d output tokens", ErrTruncated, resp.Usage.OutputTokens)
	case resp.FinishReason == "content_filter":
		return ErrContentFiltered
	case strings.TrimSpace(resp.Text) == "":
		return &Error{Retryable: true, Err: fmt.Errorf("empty reply, finish_reason %q", resp.FinishReason)}
	}
	return nil
}
