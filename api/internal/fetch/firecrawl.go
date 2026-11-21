package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	firecrawlContentType = "text/markdown; charset=utf-8"
	envelopeSlackBytes   = 1 << 20
)

type firecrawl struct {
	cfg  config.Fetch
	opts options
	http *http.Client
}

func NewFirecrawl(cfg config.Fetch, opts ...Option) Fetcher {
	return &firecrawl{cfg: cfg, opts: buildOptions(opts), http: &http.Client{Timeout: cfg.FirecrawlTimeout}}
}

type scrapeRequest struct {
	URL     string   `json:"url"`
	Formats []string `json:"formats"`
	MaxAge  int      `json:"maxAge"`
}

type scrapeResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    *struct {
		Markdown string `json:"markdown"`
		Metadata struct {
			StatusCode int    `json:"statusCode"`
			URL        string `json:"url"`
		} `json:"metadata"`
	} `json:"data"`
}

func (f *firecrawl) Fetch(ctx context.Context, req Request) (Response, error) {
	if f.cfg.FirecrawlAPIKey == "" {
		return Response{}, ErrNotConfigured
	}
	target, err := url.Parse(req.URL)
	if err != nil || !httpScheme(target) {
		return Response{}, &Error{Err: fmt.Errorf("%w: %q", ErrInvalidURL, req.URL)}
	}
	if refused := f.opts.refused(ctx, "firecrawl", req.URL, f.opts.guard().CheckHost(target.Hostname())); refused != nil {
		return Response{}, refused
	}
	payload, err := json.Marshal(scrapeRequest{URL: target.String(), Formats: []string{"markdown"}})
	if err != nil {
		return Response{}, &Error{Err: err}
	}
	endpoint := strings.TrimRight(f.cfg.FirecrawlBaseURL, "/") + "/v2/scrape"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Response{}, &Error{Err: fmt.Errorf("build firecrawl request: %w", err)}
	}
	httpReq.Header.Set("Authorization", "Bearer "+f.cfg.FirecrawlAPIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := f.http.Do(httpReq)
	if err != nil {
		return Response{}, &Error{Retryable: true, Err: fmt.Errorf("firecrawl: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	limit := effectiveMax(req, f.cfg.MaxBytes)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*limit+envelopeSlackBytes+1))
	if err != nil {
		return Response{}, &Error{StatusCode: resp.StatusCode, Retryable: true, Err: fmt.Errorf("read firecrawl response: %w", err)}
	}
	if int64(len(raw)) > 2*limit+envelopeSlackBytes {
		return Response{}, &Error{Err: fmt.Errorf("%w: limit %d bytes", ErrTooLarge, limit)}
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, firecrawlStatusError(resp, raw)
	}

	var parsed scrapeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, &Error{StatusCode: resp.StatusCode, Retryable: true, Err: fmt.Errorf("decode firecrawl response: %w", err)}
	}
	if !parsed.Success || parsed.Data == nil {
		message := parsed.Error
		if message == "" {
			message = "response has no data"
		}
		return Response{}, &Error{StatusCode: resp.StatusCode, Retryable: true, Err: errors.New("firecrawl: " + message)}
	}

	status := parsed.Data.Metadata.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	if status < 200 || status > 299 {
		return Response{}, &Error{
			StatusCode: status,
			Retryable:  retryableStatus(status),
			Err:        fmt.Errorf("%s returned %d %s via firecrawl", target.Host, status, http.StatusText(status)),
		}
	}
	if int64(len(parsed.Data.Markdown)) > limit {
		return Response{}, &Error{StatusCode: status, Err: fmt.Errorf("%w: limit %d bytes", ErrTooLarge, limit)}
	}
	finalURL := parsed.Data.Metadata.URL
	if finalURL == "" {
		finalURL = target.String()
	}
	return Response{
		Status:      status,
		ContentType: firecrawlContentType,
		Body:        []byte(parsed.Data.Markdown),
		FinalURL:    finalURL,
	}, nil
}

func firecrawlStatusError(resp *http.Response, raw []byte) *Error {
	message := http.StatusText(resp.StatusCode)
	var parsed scrapeResponse
	if json.Unmarshal(raw, &parsed) == nil && parsed.Error != "" {
		message = parsed.Error
	}
	e := &Error{
		StatusCode: resp.StatusCode,
		Retryable:  retryableStatus(resp.StatusCode),
		Err:        fmt.Errorf("firecrawl returned %d: %s", resp.StatusCode, message),
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	}
	return e
}
