package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	algRS256      = "RS256"
	clockLeeway   = 10 * time.Second
	maxTokenBytes = 8 << 10
)

var (
	ErrNotConfigured = errors.New("authentication is not configured")
	ErrInvalidToken  = errors.New("invalid token")
	ErrUnavailable   = errors.New("signing keys unavailable")
)

type Identity struct {
	UserID         string
	SessionID      string
	OrganizationID string
	Role           string
	Permissions    []string
	Email          string
	ExpiresAt      time.Time
}

type Verifier struct {
	clientID string
	issuers  []string
	audience string
	keys     *jwks
	now      func() time.Time
}

type Option func(*options)

type options struct {
	client *http.Client
	now    func() time.Time
}

func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

func New(cfg config.WorkOS, opts ...Option) (*Verifier, error) {
	if !cfg.Configured() {
		return &Verifier{}, nil
	}
	if len(cfg.Issuers) == 0 {
		return nil, errors.New("auth: at least one issuer is required")
	}
	jwksURL := cfg.JWKSURL()
	u, err := url.Parse(jwksURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("auth: JWKS URL must be https, got %q", jwksURL)
	}
	o := options{now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	client := o.client
	if client == nil {
		client = &http.Client{}
	}
	pinned := *client
	pinned.Timeout = jwksFetchTimeout
	pinned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Verifier{
		clientID: cfg.ClientID,
		issuers:  slices.Clone(cfg.Issuers),
		audience: cfg.Audience,
		keys:     newJWKS(jwksURL, &pinned, o.now),
		now:      o.now,
	}, nil
}

func (v *Verifier) Configured() bool { return v != nil && v.keys != nil }

func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	if !v.Configured() {
		return Identity{}, ErrNotConfigured
	}
	if len(token) > maxTokenBytes {
		return Identity{}, invalid("token too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Identity{}, invalid("not a compact JWS")
	}

	var header struct {
		Alg  string   `json:"alg"`
		Kid  string   `json:"kid"`
		Crit []string `json:"crit"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return Identity{}, invalid("header: " + err.Error())
	}
	if header.Alg != algRS256 {
		return Identity{}, invalid("algorithm not allowed")
	}
	if header.Kid == "" {
		return Identity{}, invalid("missing kid")
	}
	if header.Crit != nil {
		return Identity{}, invalid("critical header not supported")
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil {
		return Identity{}, invalid("signature encoding")
	}

	key, err := v.keys.key(ctx, header.Kid)
	if err != nil {
		return Identity{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return Identity{}, invalid("bad signature")
	}

	var c claims
	if err := decodeSegment(parts[1], &c); err != nil {
		return Identity{}, invalid("claims: " + err.Error())
	}
	if err := v.check(c); err != nil {
		return Identity{}, err
	}
	return Identity{
		UserID:         c.Subject,
		SessionID:      c.SessionID,
		OrganizationID: c.OrganizationID,
		Role:           c.Role,
		Permissions:    c.Permissions,
		Email:          c.Email,
		ExpiresAt:      c.ExpiresAt.Time(),
	}, nil
}

type claims struct {
	Issuer         string       `json:"iss"`
	Subject        string       `json:"sub"`
	Audience       audience     `json:"aud"`
	ClientID       string       `json:"client_id"`
	SessionID      string       `json:"sid"`
	OrganizationID string       `json:"org_id"`
	Role           string       `json:"role"`
	Permissions    []string     `json:"permissions"`
	Email          string       `json:"email"`
	SubProfile     *string      `json:"sub_profile"`
	SubType        *string      `json:"sub_type"`
	ExpiresAt      *numericDate `json:"exp"`
	IssuedAt       *numericDate `json:"iat"`
	NotBefore      *numericDate `json:"nbf"`
}

func (v *Verifier) check(c claims) error {
	now := v.now()
	switch {
	case !slices.Contains(v.issuers, c.Issuer):
		return invalid("issuer")
	case c.ClientID != v.clientID:
		return invalid("client")
	case v.audience != "" && !slices.Contains(c.Audience, v.audience):
		return invalid("audience")
	case c.Subject == "":
		return invalid("missing sub")
	case c.SessionID == "":
		return invalid("missing sid")
	case c.SubProfile != nil && *c.SubProfile != "user":
		return invalid("not a user token")
	case c.SubType != nil:
		return invalid("not a user token")
	case c.ExpiresAt == nil:
		return invalid("missing exp")
	case !now.Before(c.ExpiresAt.Time().Add(clockLeeway)):
		return invalid("expired")
	case c.IssuedAt == nil:
		return invalid("missing iat")
	case c.IssuedAt.Time().After(now.Add(clockLeeway)):
		return invalid("issued in the future")
	case c.NotBefore != nil && c.NotBefore.Time().After(now.Add(clockLeeway)):
		return invalid("not yet valid")
	}
	return nil
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidToken, reason)
}

func decodeSegment(segment string, out any) error {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(segment)
	if err != nil {
		return errors.New("encoding")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return errors.New("json")
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

type numericDate struct{ seconds float64 }

func (d *numericDate) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || b[0] == '"' {
		return errors.New("numeric date must be a number")
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > math.MaxInt32*16 {
		return errors.New("bad numeric date")
	}
	d.seconds = f
	return nil
}

func (d *numericDate) Time() time.Time {
	sec, frac := math.Modf(d.seconds)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}
