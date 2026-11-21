package webhooks

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/netguard"
)

const (
	DefaultRequestTimeout = 15 * time.Second
	maxResponseBytes      = 64 << 10
)

var ErrForbiddenAddress = netguard.ErrForbiddenAddress

type Request struct {
	URL    string
	ID     string
	Body   []byte
	Secret config.Secret
	At     time.Time
}

type Result struct {
	StatusCode int
	Duration   time.Duration
	Err        error
}

func (r Result) Succeeded() bool {
	return r.Err == nil && r.StatusCode >= 200 && r.StatusCode <= 299
}

func (r Result) Refused() bool { return errors.Is(r.Err, ErrForbiddenAddress) }

func (r Result) Problem() string {
	switch {
	case r.Err != nil:
		return r.Err.Error()
	case !r.Succeeded():
		return fmt.Sprintf("the endpoint answered %d", r.StatusCode)
	default:
		return ""
	}
}

type Sender struct {
	client    *http.Client
	dialer    netguard.Dialer
	userAgent string
	timeout   time.Duration
}

type SenderOption func(*Sender, *tls.Config)

func AllowAddress(allow func(netip.Addr) bool) SenderOption {
	return func(s *Sender, _ *tls.Config) { s.dialer.Allow = allow }
}

func WithRootCAs(pool *x509.CertPool) SenderOption {
	return func(_ *Sender, c *tls.Config) { c.RootCAs = pool }
}

func WithTimeout(d time.Duration) SenderOption {
	return func(s *Sender, _ *tls.Config) {
		if d > 0 {
			s.timeout = d
		}
	}
}

func NewSender(userAgent string, opts ...SenderOption) *Sender {
	s := &Sender{userAgent: userAgent, timeout: DefaultRequestTimeout}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	for _, opt := range opts {
		opt(s, tlsConfig)
	}
	s.dialer.Timeout = s.timeout
	s.client = &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            s.dialer.DialContext,
			TLSClientConfig:        tlsConfig,
			TLSHandshakeTimeout:    s.timeout,
			ResponseHeaderTimeout:  s.timeout,
			MaxResponseHeaderBytes: maxResponseBytes,
			ForceAttemptHTTP2:      true,
			MaxIdleConns:           20,
			IdleConnTimeout:        90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return s
}

func (s *Sender) Send(ctx context.Context, req Request) Result {
	start := time.Now()
	result := s.send(ctx, req)
	result.Duration = time.Since(start)
	return result
}

func (s *Sender) send(ctx context.Context, req Request) Result {
	target, err := url.Parse(req.URL)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" {
		return Result{Err: errors.New("the endpoint URL is not an https URL")}
	}
	if err := s.dialer.CheckHost(target.Hostname()); err != nil {
		return Result{Err: err}
	}
	signature, err := Sign(req.Secret, req.ID, req.At, req.Body)
	if err != nil {
		return Result{Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(req.Body))
	if err != nil {
		return Result{Err: errors.New("the endpoint URL cannot be requested")}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", s.userAgent)
	httpReq.Header.Set(HeaderID, req.ID)
	httpReq.Header.Set(HeaderTimestamp, strconv.FormatInt(req.At.Unix(), 10))
	httpReq.Header.Set(HeaderSignature, signature)
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return Result{Err: requestError(ctx, err)}
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	return Result{StatusCode: resp.StatusCode}
}

func requestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the endpoint did not answer in time: %w", context.DeadlineExceeded)
	}
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		return urlErr.Err
	}
	return err
}
