package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

type plain struct {
	cfg  config.Fetch
	opts options
	http *http.Client
}

func NewPlain(cfg config.Fetch, opts ...Option) Fetcher {
	p := &plain{cfg: cfg, opts: buildOptions(opts)}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = p.opts.guard().DialContext
	p.http = &http.Client{Timeout: cfg.Timeout, Transport: transport, CheckRedirect: p.checkRedirect}
	return p
}

func (p *plain) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > p.cfg.MaxRedirects {
		return ErrTooManyRedirects
	}
	if !httpScheme(req.URL) {
		return fmt.Errorf("%w: redirect to %q", ErrInvalidURL, req.URL.Scheme)
	}
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	return nil
}

func httpScheme(u *url.URL) bool {
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func effectiveMax(req Request, fallback int64) int64 {
	if req.MaxBytes > 0 {
		return req.MaxBytes
	}
	return fallback
}

func (p *plain) Fetch(ctx context.Context, req Request) (Response, error) {
	target, err := url.Parse(req.URL)
	if err != nil || !httpScheme(target) {
		return Response{}, &Error{Err: fmt.Errorf("%w: %q", ErrInvalidURL, req.URL)}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Response{}, &Error{Err: fmt.Errorf("%w: %q", ErrInvalidURL, req.URL)}
	}
	httpReq.Header.Set("User-Agent", p.cfg.UserAgent)

	resp, err := p.http.Do(httpReq)
	if err != nil {
		if refused := p.opts.refused(ctx, "plain", req.URL, err); refused != nil {
			return Response{}, refused
		}
		return Response{}, transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Response{}, statusError(resp)
	}

	limit := effectiveMax(req, p.cfg.MaxBytes)
	if resp.ContentLength > limit {
		return Response{}, &Error{StatusCode: resp.StatusCode, Err: fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, resp.ContentLength, limit)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return Response{}, &Error{StatusCode: resp.StatusCode, Retryable: true, Err: fmt.Errorf("read body: %w", err)}
	}
	if int64(len(body)) > limit {
		return Response{}, &Error{StatusCode: resp.StatusCode, Err: fmt.Errorf("%w: limit %d bytes", ErrTooLarge, limit)}
	}
	return Response{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		FinalURL:    resp.Request.URL.String(),
	}, nil
}

func transportError(err error) *Error {
	switch {
	case errors.Is(err, ErrTooManyRedirects):
		return &Error{Err: ErrTooManyRedirects}
	case errors.Is(err, ErrInvalidURL):
		return &Error{Err: err}
	}
	return &Error{Retryable: true, Err: err}
}

func statusError(resp *http.Response) *Error {
	e := &Error{
		StatusCode: resp.StatusCode,
		Retryable:  retryableStatus(resp.StatusCode),
		Err:        fmt.Errorf("%s returned %d %s", resp.Request.URL.Host, resp.StatusCode, http.StatusText(resp.StatusCode)),
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	}
	return e
}
