package media

import (
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

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/netguard"
)

const (
	defaultPageTimeout = 30 * time.Second
	maxRedirects       = 5
	dialTimeout        = 10 * time.Second
)

var (
	ErrForbiddenAddress = netguard.ErrForbiddenAddress
	ErrForbiddenHost    = errors.New("media: host or port is not an allowed recording host")
)

type Guard struct {
	AllowAddress func(netip.Addr) bool
	AllowPort    func(scheme, port string) bool
	RootCAs      *x509.CertPool
}

func (g Guard) portAllowed(scheme, port string) bool {
	if g.AllowPort != nil {
		return g.AllowPort(scheme, port)
	}
	switch scheme {
	case "https":
		return port == "" || port == "443"
	case "http":
		return port == "" || port == "80"
	}
	return false
}

func (g Guard) dialer() netguard.Dialer {
	return netguard.Dialer{Allow: g.AllowAddress, Timeout: dialTimeout}
}

func (g Guard) Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           g.dialer().DialContext,
		TLSClientConfig:       &tls.Config{RootCAs: g.RootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: defaultPageTimeout,
		DisableCompression:    true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
}

func (g Guard) checkURL(u *url.URL, hosts []string) error {
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: %q", ErrForbiddenHost, u.String())
	}
	if !matchHost(hosts, u.Hostname()) {
		return fmt.Errorf("%w: %s", ErrForbiddenHost, u.Hostname())
	}
	if !g.portAllowed(u.Scheme, u.Port()) {
		return fmt.Errorf("%w: port %s for %s", ErrForbiddenHost, u.Port(), u.Scheme)
	}
	return nil
}

func (g Guard) client(hosts []string, userAgent string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: g.Transport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fetch.ErrTooManyRedirects
			}
			if err := g.checkURL(req.URL, hosts); err != nil {
				return err
			}
			if userAgent != "" {
				req.Header.Set("User-Agent", userAgent)
			}
			return nil
		},
	}
}

type PageFetcher struct {
	Hosts     []string
	Guard     Guard
	UserAgent string
	Timeout   time.Duration
}

func (p PageFetcher) Fetch(ctx context.Context, req fetch.Request) (fetch.Response, error) {
	target, err := url.Parse(req.URL)
	if err != nil {
		return fetch.Response{}, &fetch.Error{Err: fmt.Errorf("%w: %q", fetch.ErrInvalidURL, req.URL)}
	}
	if err := p.Guard.checkURL(target, p.Hosts); err != nil {
		return fetch.Response{}, &fetch.Error{Err: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fetch.Response{}, &fetch.Error{Err: fmt.Errorf("%w: %q", fetch.ErrInvalidURL, req.URL)}
	}
	if p.UserAgent != "" {
		httpReq.Header.Set("User-Agent", p.UserAgent)
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultPageTimeout
	}
	resp, err := p.Guard.client(p.Hosts, p.UserAgent, timeout).Do(httpReq)
	if err != nil {
		return fetch.Response{}, &fetch.Error{Retryable: retryableTransport(err), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fetch.Response{}, statusError(resp)
	}
	limit := req.MaxBytes
	if limit <= 0 {
		limit = maxPlayerPageBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fetch.Response{}, &fetch.Error{StatusCode: resp.StatusCode, Retryable: true, Err: fmt.Errorf("read body: %w", err)}
	}
	if int64(len(body)) > limit {
		return fetch.Response{}, &fetch.Error{StatusCode: resp.StatusCode, Err: fmt.Errorf("%w: limit %d bytes", fetch.ErrTooLarge, limit)}
	}
	return fetch.Response{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        body,
		FinalURL:    resp.Request.URL.String(),
	}, nil
}

func retryableTransport(err error) bool {
	return !errors.Is(err, ErrForbiddenHost) && !errors.Is(err, ErrForbiddenAddress) && !errors.Is(err, fetch.ErrTooManyRedirects)
}

func statusError(resp *http.Response) *fetch.Error {
	code := resp.StatusCode
	e := &fetch.Error{
		StatusCode: code,
		Retryable:  code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500,
		Err:        fmt.Errorf("%s returned %d %s", resp.Request.URL.Host, code, http.StatusText(code)),
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	}
	return e
}
