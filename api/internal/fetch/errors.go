package fetch

import (
	"errors"
	"net/http"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/netguard"
)

var (
	ErrNotConfigured    = errors.New("fetch: firecrawl not configured, set FIRECRAWL_API_KEY")
	ErrTooLarge         = errors.New("fetch: response exceeds size limit")
	ErrTooManyRedirects = errors.New("fetch: too many redirects")
	ErrInvalidURL       = errors.New("fetch: invalid url")
	ErrForbiddenAddress = netguard.ErrForbiddenAddress
)

type Error struct {
	StatusCode int
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string { return "fetch: " + e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

func IsRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable
}

func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}
