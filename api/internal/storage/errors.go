package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/minio/minio-go/v7"
)

var (
	ErrNotConfigured = errors.New("storage: not configured, set STORAGE_ENDPOINT, STORAGE_BUCKET, STORAGE_ACCESS_KEY_ID and STORAGE_SECRET_ACCESS_KEY")
	ErrNotFound      = errors.New("storage: object not found")
)

type Error struct {
	StatusCode int
	Retryable  bool
	Err        error
}

func (e *Error) Error() string { return "storage: " + e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

func IsRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable
}

func classify(err error, key string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Retryable: true, Err: err}
	}
	resp := minio.ToErrorResponse(err)
	if resp.StatusCode == 0 {
		return &Error{Retryable: true, Err: err}
	}
	if resp.Code == "NoSuchKey" {
		return fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	retryable := resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= http.StatusInternalServerError
	return &Error{StatusCode: resp.StatusCode, Retryable: retryable, Err: err}
}
