package llm

import (
	"errors"
	"time"
)

var (
	ErrNotConfigured = errors.New("llm: not configured, set AI_GATEWAY_API_KEY")
	ErrUnknownTier   = errors.New("llm: unknown tier")
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
