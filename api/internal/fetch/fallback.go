package fetch

import (
	"context"
	"errors"
	"net/http"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

type fallback struct {
	primary   Fetcher
	secondary Fetcher
}

func New(cfg config.Fetch) Fetcher {
	return NewFallback(NewPlain(cfg), NewFirecrawl(cfg))
}

func NewFallback(primary, secondary Fetcher) Fetcher {
	return &fallback{primary: primary, secondary: secondary}
}

func (f *fallback) Fetch(ctx context.Context, req Request) (Response, error) {
	resp, err := f.primary.Fetch(ctx, req)
	if err == nil || !botBlocked(err) {
		return resp, err
	}
	resp, secondaryErr := f.secondary.Fetch(ctx, req)
	if errors.Is(secondaryErr, ErrNotConfigured) {
		return Response{}, err
	}
	return resp, secondaryErr
}

func botBlocked(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotAcceptable, http.StatusTooManyRequests, 999:
		return true
	}
	return false
}
