package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotConfigured    = errors.New("llm: not configured, set AI_GATEWAY_API_KEY")
	ErrUnknownTier      = errors.New("llm: unknown tier")
	ErrTruncated        = errors.New("llm: reply truncated at the output token limit")
	ErrContentFiltered  = errors.New("llm: reply blocked by the content filter")
	ErrResponseTooLarge = errors.New("llm: gateway response too large")
)

type Error struct {
	StatusCode int
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string { return "llm: " + e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

func IsRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable
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
