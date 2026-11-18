package media

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	proxyReadHeaderTimeout = 30 * time.Second
	tunnelIdleTimeout      = 60 * time.Second
)

var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

type Proxy struct {
	hosts       []string
	guard       Guard
	maxRequests int64
	transport   *http.Transport
	listener    net.Listener
	server      *http.Server

	requests atomic.Int64
	mu       sync.Mutex
	refusals []string
	flooded  bool
}

func StartProxy(hosts []string, guard Guard, maxRequests int64) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%w: start media proxy: %w", ErrUnavailable, err)
	}
	p := &Proxy{hosts: hosts, guard: guard, maxRequests: maxRequests, transport: guard.Transport(), listener: ln}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: proxyReadHeaderTimeout}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

func (p *Proxy) URL() string { return "http://" + p.listener.Addr().String() }

func (p *Proxy) Close() {
	_ = p.server.Close()
	p.transport.CloseIdleConnections()
}

func (p *Proxy) Requests() int64 { return p.requests.Load() }

func (p *Proxy) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case len(p.refusals) > 0:
		return fmt.Errorf("%w: %s", ErrForbiddenHost, strings.Join(p.refusals, "; "))
	case p.flooded:
		return fmt.Errorf("%w: more than %d requests for one recording", ErrLimitExceeded, p.maxRequests)
	}
	return nil
}

func (p *Proxy) refuse(w http.ResponseWriter, reason string) {
	p.mu.Lock()
	if len(p.refusals) < 5 {
		p.refusals = append(p.refusals, reason)
	}
	p.mu.Unlock()
	http.Error(w, "refused by media proxy", http.StatusForbidden)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.requests.Add(1) > p.maxRequests {
		p.mu.Lock()
		p.flooded = true
		p.mu.Unlock()
		http.Error(w, "request limit reached", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	p.forward(w, r)
}

func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || !p.guard.portAllowed(port) || !matchHost(p.hosts, host) {
		p.refuse(w, "CONNECT "+r.Host)
		return
	}
	upstream, err := p.guard.Dialer().DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		p.dialFailed(w, r.Host, err)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	go pipe(upstream, buffered)
	pipe(client, upstream)
}

func pipe(dst net.Conn, src io.Reader) {
	defer func() { _ = dst.Close() }()
	_ = dst.SetDeadline(time.Now().Add(tunnelIdleTimeout))
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			_ = dst.SetDeadline(time.Now().Add(tunnelIdleTimeout))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Proxy) dialFailed(w http.ResponseWriter, target string, err error) {
	if errors.Is(err, ErrForbiddenAddress) {
		p.refuse(w, target+": "+err.Error())
		return
	}
	http.Error(w, "upstream unreachable", http.StatusBadGateway)
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Scheme != "http" || r.URL.Host == "" || !matchHost(p.hosts, r.URL.Hostname()) {
		p.refuse(w, r.Method+" "+r.URL.String())
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		p.dialFailed(w, r.URL.Host, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
