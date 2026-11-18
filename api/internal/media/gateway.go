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
	playlistContentType      = "application/vnd.apple.mpegurl"
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

type route struct {
	upstream string
	body     []byte
}

type Gateway struct {
	cfg      GatewayConfig
	upstream *http.Client
	listener net.Listener
	server   *http.Server

	requests atomic.Int64
	mu       sync.Mutex
	routes   []route
	refusals []string
	flooded  bool
}

func StartGateway(cfg GatewayConfig) (*Gateway, error) {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%w: start media gateway: %w", ErrUnavailable, err)
	}
	upstream := cfg.Guard.client(cfg.Hosts, cfg.UserAgent, 0)
	g := &Gateway{cfg: cfg, upstream: upstream, listener: ln}
	g.server = &http.Server{Handler: g, ReadHeaderTimeout: gatewayReadHeaderTimeout}
	go func() { _ = g.server.Serve(ln) }()
	return g, nil
}

func (g *Gateway) Close() {
	_ = g.server.Close()
	if t, ok := g.upstream.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

func (g *Gateway) Requests() int64 { return g.requests.Load() }

func (g *Gateway) add(r route, name string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes = append(g.routes, r)
	name = strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			return c
		}
		return '_'
	}, name)
	if name == "" || name == "." || name == "/" {
		name = "media"
	}
	return fmt.Sprintf("http://%s%s%d/%s", g.listener.Addr(), routePrefix, len(g.routes)-1, name)
}

func (g *Gateway) Register(upstream string) string {
	return g.add(route{upstream: upstream}, path.Base(upstreamPath(upstream)))
}

func (g *Gateway) Serve(body []byte, name string) string {
	return g.add(route{body: body}, name)
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

func (g *Gateway) refuse(w http.ResponseWriter, reason string) {
	g.mu.Lock()
	if len(g.refusals) < 5 {
		g.refusals = append(g.refusals, reason)
	}
	g.mu.Unlock()
	http.Error(w, "refused by media gateway", http.StatusForbidden)
}

func (g *Gateway) lookup(p string) (route, bool) {
	rest, ok := strings.CutPrefix(p, routePrefix)
	if !ok {
		return route{}, false
	}
	idx, _, _ := strings.Cut(rest, "/")
	n, err := strconv.Atoi(idx)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil || n < 0 || n >= len(g.routes) {
		return route{}, false
	}
	return g.routes[n], true
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.requests.Add(1) > g.cfg.MaxRequests {
		g.mu.Lock()
		g.flooded = true
		g.mu.Unlock()
		http.Error(w, "request limit reached", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		g.refuse(w, r.Method+" "+r.URL.Path)
		return
	}
	rt, ok := g.lookup(r.URL.Path)
	if !ok {
		g.refuse(w, "unknown path "+r.URL.Path)
		return
	}
	if rt.body != nil {
		w.Header().Set("Content-Type", playlistContentType)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(rt.body))
		return
	}
	g.forward(w, r, rt.upstream)
}

func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, upstream string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	idle := time.AfterFunc(g.cfg.IdleTimeout, cancel)
	defer idle.Stop()

	resp, refusal, err := g.fetch(ctx, r, upstream)
	switch {
	case refusal != "":
		g.refuse(w, refusal)
		return
	case err != nil:
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	head, rerr := g.readHead(resp.Body, idle)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	if looksLikePlaylist(head) {
		g.refuse(w, "playlist served as media by "+upstream)
		return
	}
	for _, h := range passedHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	if len(head) > 0 {
		if _, err := w.Write(head); err != nil {
			return
		}
	}
	if rerr != nil {
		return
	}
	g.stream(w, resp.Body, idle)
}

func (g *Gateway) fetch(ctx context.Context, r *http.Request, upstream string) (*http.Response, string, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, "bad upstream " + upstream, nil
	}
	for hop := 0; ; hop++ {
		if err := g.cfg.Guard.checkURL(target, g.cfg.Hosts); err != nil {
			return nil, err.Error(), nil
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), nil) //nolint:gosec
		if err != nil {
			return nil, "bad upstream " + target.String(), nil
		}
		if rng := r.Header.Get("Range"); rng != "" {
			req.Header.Set("Range", rng)
		}
		req.Header.Set("Accept-Encoding", "identity")
		if g.cfg.UserAgent != "" {
			req.Header.Set("User-Agent", g.cfg.UserAgent)
		}
		resp, err := g.upstream.Transport.RoundTrip(req)
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

func (g *Gateway) stream(w http.ResponseWriter, body io.Reader, idle *time.Timer) {
	rc := http.NewResponseController(w)
	buf := make([]byte, copyBufferBytes)
	for {
		idle.Reset(g.cfg.IdleTimeout)
		n, err := body.Read(buf)
		idle.Stop()
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(g.cfg.IdleTimeout))
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
