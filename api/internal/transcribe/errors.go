package transcribe

import (
	"errors"
	"time"
)

var (
	ErrNotConfigured    = errors.New("transcribe: not configured, set AI_GATEWAY_API_KEY, or GROQ_API_KEY for the groq provider")
	ErrResponseTooLarge = errors.New("transcribe: response too large")
	ErrChunkTooLarge    = errors.New("transcribe: audio chunk exceeds the upload limit")
	ErrInvalidChunking  = errors.New("transcribe: chunk overlap must be shorter than the chunk length")
)

type Error struct {
	StatusCode int
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string { return "transcribe: " + e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

func IsRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable
}
