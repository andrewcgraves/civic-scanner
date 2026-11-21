package webhookstest

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

var (
	ErrMissingHeader = errors.New("a webhook header is missing")
	ErrBadSignature  = errors.New("no signature matches")
)

type Delivery struct {
	Header    http.Header
	Body      []byte
	Timestamp time.Time
	VerifyErr error
}

type Receiver struct {
	Server *httptest.Server

	mu         sync.Mutex
	secret     string
	respond    func(n int) int
	deliveries []Delivery
}

func NewReceiver(t testing.TB, secret string, respond func(n int) int) *Receiver {
	t.Helper()
	if respond == nil {
		respond = func(int) int { return http.StatusNoContent }
	}
	r := &Receiver{secret: secret, respond: respond}
	r.Server = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Server.Close)
	return r
}

func (r *Receiver) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	secret := r.secret
	r.mu.Unlock()
	ts, err := Verify(secret, req.Header, body)
	r.mu.Lock()
	r.deliveries = append(r.deliveries, Delivery{Header: req.Header.Clone(), Body: body, Timestamp: ts, VerifyErr: err})
	n := len(r.deliveries)
	r.mu.Unlock()
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(r.respond(n))
}

func (r *Receiver) SetSecret(secret string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secret = secret
}

func (r *Receiver) URL() string { return r.Server.URL + "/hooks?token=receiver-token" }

func (r *Receiver) Deliveries() []Delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Delivery(nil), r.deliveries...)
}

func (r *Receiver) SenderOptions() []webhooks.SenderOption {
	pool := x509.NewCertPool()
	pool.AddCert(r.Server.Certificate())
	return []webhooks.SenderOption{webhooks.AllowAddress(netip.Addr.IsLoopback), webhooks.WithRootCAs(pool)}
}

func Verify(secret string, header http.Header, body []byte) (time.Time, error) {
	id, timestamp, signatures := header.Get("webhook-id"), header.Get("webhook-timestamp"), header.Get("webhook-signature")
	if id == "" || timestamp == "" || signatures == "" {
		return time.Time{}, ErrMissingHeader
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return time.Time{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, id+"."+timestamp+".")
	_, _ = mac.Write(body)
	want := mac.Sum(nil)
	for _, candidate := range strings.Fields(signatures) {
		version, encoded, ok := strings.Cut(candidate, ",")
		if !ok || version != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && hmac.Equal(got, want) {
			return time.Unix(seconds, 0).UTC(), nil
		}
	}
	return time.Time{}, ErrBadSignature
}
