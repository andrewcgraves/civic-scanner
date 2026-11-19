package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	gatewayReadHeaderTimeout = 30 * time.Second
	DefaultIdleTimeout       = 60 * time.Second
	routePrefix              = "/r/"
	copyBufferBytes          = 32 << 10
	sniffBytes               = 64
)

var passedHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "Etag"}

type GatewayConfig struct {
	Hosts       []string
	Guard       Guard
	UserAgent   string
	MaxRequests int64
	IdleTimeout time.Duration
}

type Gateway struct {
	cfg      GatewayConfig
	upstream http.RoundTripper
	listener net.Listener
	server   *http.Server

	requests atomic.Int64
	mu       sync.Mutex
	routes   []string
	refusals []string
	flooded  bool
}

type StatusError struct {
	URL    string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s answered %d %s", e.URL, e.Status, http.StatusText(e.Status))
}

func (e *StatusError) Unwrap() error {
	if e.Status == http.StatusRequestTimeout || e.Status == http.StatusTooManyRequests || e.Status >= 500 {
		return ErrNetwork
	}
	return ErrRefused
}

func StartGateway(cfg GatewayConfig) (*Gateway, error) {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%w: start media gateway: %w", ErrUnavailable, err)
	}
	g := &Gateway{cfg: cfg, upstream: cfg.Guard.Transport(), listener: ln}
	g.server = &http.Server{Handler: g, ReadHeaderTimeout: gatewayReadHeaderTimeout}
	go func() { _ = g.server.Serve(ln) }()
	return g, nil
}

func (g *Gateway) Close() {
	_ = g.server.Close()
	if t, ok := g.upstream.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

func (g *Gateway) Requests() int64 { return g.requests.Load() }

func (g *Gateway) Register(upstream string) string {
	name := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			return c
		}
		return '_'
	}, path.Base(upstreamPath(upstream)))
	if name == "" || name == "." || name == "_" {
		name = "media"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes = append(g.routes, upstream)
	return fmt.Sprintf("http://%s%s%d/%s", g.listener.Addr(), routePrefix, len(g.routes)-1, name)
}

func upstreamPath(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		return raw[i:]
	}
	return "/"
}

func (g *Gateway) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case len(g.refusals) > 0:
		return fmt.Errorf("%w: %s", ErrForbiddenHost, strings.Join(g.refusals, "; "))
	case g.flooded:
		return fmt.Errorf("%w: more than %d requests for one recording", ErrLimitExceeded, g.cfg.MaxRequests)
	}
	return nil
}

func (g *Gateway) record(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.refusals) < 5 {
		g.refusals = append(g.refusals, reason)
	}
}

func (g *Gateway) refuse(w http.ResponseWriter, reason string) {
	g.record(reason)
	http.Error(w, "refused by media gateway", http.StatusForbidden)
}

func (g *Gateway) count() bool {
	if g.requests.Add(1) <= g.cfg.MaxRequests {
		return true
	}
	g.mu.Lock()
	g.flooded = true
	g.mu.Unlock()
	return false
}

func (g *Gateway) lookup(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, routePrefix)
	if !ok {
		return "", false
	}
	idx, _, _ := strings.Cut(rest, "/")
	n, err := strconv.Atoi(idx)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil || n < 0 || n >= len(g.routes) {
		return "", false
	}
	return g.routes[n], true
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.count() {
		http.Error(w, "request limit reached", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		g.refuse(w, r.Method+" "+r.URL.Path)
		return
	}
	upstream, ok := g.lookup(r.URL.Path)
	if !ok {
		g.refuse(w, "unknown path "+r.URL.Path)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	idle := time.AfterFunc(g.cfg.IdleTimeout, cancel)
	defer idle.Stop()

	resp, head, refusal, err := g.open(ctx, r.Method, r.Header.Get("Range"), upstream, idle)
	switch {
	case refusal != "":
		g.refuse(w, refusal)
		return
	case err != nil:
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for _, h := range passedHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	rc := http.NewResponseController(w)
	_ = g.copy(resp.Body, head, idle, func(p []byte) error {
		_ = rc.SetWriteDeadline(time.Now().Add(g.cfg.IdleTimeout))
		_, err := w.Write(p) //nolint:gosec
		return err
	})
}

func (g *Gateway) Copy(ctx context.Context, upstream string, w io.Writer) error {
	if !g.count() {
		return fmt.Errorf("%w: more than %d requests for one recording", ErrLimitExceeded, g.cfg.MaxRequests)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(g.cfg.IdleTimeout, cancel)
	defer idle.Stop()

	resp, head, refusal, err := g.open(ctx, http.MethodGet, "", upstream, idle)
	switch {
	case refusal != "":
		g.record(refusal)
		return fmt.Errorf("%w: %s", ErrForbiddenHost, refusal)
	case err != nil:
		return fmt.Errorf("%w: %s: %w", ErrNetwork, upstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{URL: upstream, Status: resp.StatusCode}
	}
	var writeErr error
	err = g.copy(resp.Body, head, idle, func(p []byte) error {
		_, writeErr = w.Write(p)
		return writeErr
	})
	switch {
	case writeErr != nil:
		return writeErr
	case err != nil:
		return fmt.Errorf("%w: %s: %w", ErrNetwork, upstream, err)
	}
	return nil
}

func (g *Gateway) open(ctx context.Context, method, rng, upstream string, idle *time.Timer) (*http.Response, []byte, string, error) {
	resp, refusal, err := g.fetch(ctx, method, rng, upstream)
	if refusal != "" || err != nil {
		return nil, nil, refusal, err
	}
	head, rerr := g.readHead(resp.Body, idle)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		_ = resp.Body.Close()
		return nil, nil, "", rerr
	}
	if looksLikePlaylist(head) {
		_ = resp.Body.Close()
		return nil, nil, "playlist served as media by " + upstream, nil
	}
	if errors.Is(rerr, io.EOF) {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
	}
	return resp, head, "", nil
}

func (g *Gateway) fetch(ctx context.Context, method, rng, upstream string) (*http.Response, string, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, "bad upstream " + upstream, nil
	}
	for hop := 0; ; hop++ {
		if err := g.cfg.Guard.checkURL(target, g.cfg.Hosts); err != nil {
			return nil, err.Error(), nil
		}
		req, err := http.NewRequestWithContext(ctx, method, target.String(), nil) //nolint:gosec
		if err != nil {
			return nil, "bad upstream " + target.String(), nil
		}
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		req.Header.Set("Accept-Encoding", "identity")
		if g.cfg.UserAgent != "" {
			req.Header.Set("User-Agent", g.cfg.UserAgent)
		}
		resp, err := g.upstream.RoundTrip(req)
		if err != nil {
			if errors.Is(err, ErrForbiddenHost) || errors.Is(err, ErrForbiddenAddress) {
				return nil, err.Error(), nil
			}
			return nil, "", err
		}
		location := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode > 399 || location == "" {
			return resp, "", nil
		}
		_ = resp.Body.Close()
		if hop >= maxRedirects {
			return nil, "too many redirects from " + upstream, nil
		}
		next, err := target.Parse(location)
		if err != nil {
			return nil, "redirect to an unreadable location " + strconv.Quote(location), nil
		}
		target = next
	}
}

func (g *Gateway) readHead(body io.Reader, idle *time.Timer) ([]byte, error) {
	head := make([]byte, 0, sniffBytes)
	buf := make([]byte, sniffBytes)
	for len(head) < sniffBytes {
		idle.Reset(g.cfg.IdleTimeout)
		n, err := body.Read(buf[:sniffBytes-len(head)])
		idle.Stop()
		head = append(head, buf[:n]...)
		if err != nil {
			return head, err
		}
	}
	return head, nil
}

func looksLikePlaylist(head []byte) bool {
	head = bytes.TrimLeft(bytes.TrimPrefix(head, []byte{0xef, 0xbb, 0xbf}), " \t\r\n")
	return bytes.HasPrefix(bytes.ToUpper(head), []byte("#EXTM3U"))
}

func (g *Gateway) copy(body io.Reader, head []byte, idle *time.Timer, write func([]byte) error) error {
	if len(head) > 0 {
		if err := write(head); err != nil {
			return err
		}
	}
	buf := make([]byte, copyBufferBytes)
	for {
		idle.Reset(g.cfg.IdleTimeout)
		n, err := body.Read(buf)
		idle.Stop()
		if n > 0 {
			if werr := write(buf[:n]); werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
