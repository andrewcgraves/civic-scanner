package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	jwksTTL             = time.Hour
	jwksMaxStale        = 24 * time.Hour
	jwksMinRefresh      = 10 * time.Second
	jwksFetchTimeout    = 5 * time.Second
	jwksMaxBytes        = 1 << 20
	minRSAModulusBits   = 2048
	maxRSAModulusBits   = 8192
	maxRSAPublicExpBits = 32
)

var (
	errUnknownKey = errors.New("unknown signing key")
	errNoKeys     = errors.New("key set has no usable RS256 keys")
)

type keySet struct {
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

type jwks struct {
	url    string
	client *http.Client
	now    func() time.Time
	logger *slog.Logger

	mu          sync.Mutex
	set         *keySet
	lastAttempt time.Time
	lastErr     error
	inflight    chan struct{}
}

func newJWKS(url string, client *http.Client, now func() time.Time, logger *slog.Logger) *jwks {
	return &jwks{url: url, client: client, now: now, logger: logger}
}

func (j *jwks) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.Lock()
	set := j.set
	now := j.now()
	if set != nil && set.keys[kid] != nil && now.Sub(set.fetchedAt) < jwksTTL {
		j.mu.Unlock()
		return set.keys[kid], nil
	}
	if set != nil && set.keys[kid] != nil && now.Sub(set.fetchedAt) < jwksMaxStale {
		j.startRefreshLocked(now)
		j.mu.Unlock()
		return set.keys[kid], nil
	}
	wait := j.startRefreshLocked(now)
	j.mu.Unlock()

	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
		}
	}
	return j.lookup(kid, wait != nil)
}

func (j *jwks) lookup(kid string, refreshed bool) (*rsa.PublicKey, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	set := j.set
	if set == nil || j.now().Sub(set.fetchedAt) >= jwksMaxStale {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, j.lastErr)
	}
	if key := set.keys[kid]; key != nil {
		return key, nil
	}
	if j.lastErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, j.lastErr)
	}
	if !refreshed {
		return nil, fmt.Errorf("%w: %w until the key set can be refreshed", ErrUnavailable, errUnknownKey)
	}
	return nil, fmt.Errorf("%w: %w", ErrInvalidToken, errUnknownKey)
}

func (j *jwks) startRefreshLocked(now time.Time) chan struct{} {
	if j.inflight != nil {
		return j.inflight
	}
	if !j.lastAttempt.IsZero() && now.Sub(j.lastAttempt) < jwksMinRefresh {
		return nil
	}
	j.lastAttempt = now
	done := make(chan struct{})
	j.inflight = done
	go j.refresh(done)
	return done
}

func (j *jwks) refresh(done chan struct{}) {
	keys, err := j.fetch()
	j.mu.Lock()
	if err != nil {
		j.lastErr = err
		j.logger.Warn("WorkOS key set refresh failed", "error", err)
	} else {
		j.set = &keySet{keys: keys, fetchedAt: j.now()}
		j.lastErr = nil
	}
	j.inflight = nil
	j.mu.Unlock()
	close(done)
}

func (j *jwks) fetch() (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), jwksFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jwks read: %w", err)
	}
	if len(body) > jwksMaxBytes {
		return nil, fmt.Errorf("jwks read: body over %d bytes", jwksMaxBytes)
	}
	return parseJWKS(body)
}

type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func parseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var doc struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("jwks decode: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != algRS256) {
			continue
		}
		if _, dup := keys[k.Kid]; dup {
			continue
		}
		pub, err := rsaPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errNoKeys
	}
	return keys, nil
}

func rsaPublicKey(n, e string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil {
		return nil, err
	}
	modulus := new(big.Int).SetBytes(nBytes)
	exponent := new(big.Int).SetBytes(eBytes)
	if bits := modulus.BitLen(); bits < minRSAModulusBits || bits > maxRSAModulusBits {
		return nil, fmt.Errorf("modulus of %d bits", modulus.BitLen())
	}
	if exponent.BitLen() > maxRSAPublicExpBits-1 || exponent.Int64() < 3 || exponent.Bit(0) == 0 {
		return nil, errors.New("bad public exponent")
	}
	return &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}, nil
}
