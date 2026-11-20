package auth

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	testClientID = "client_test"
	testIssuer   = "https://api.workos.com/user_management/client_test"
)

var (
	keyOnce  sync.Once
	testKeys []*rsa.PrivateKey
)

func rsaKey(t testing.TB, i int) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		for range 3 {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			testKeys = append(testKeys, k)
		}
	})
	return testKeys[i]
}

type clock struct{ now atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.now.Store(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *clock) Now() time.Time             { return time.Unix(0, c.now.Load()).UTC() }
func (c *clock) Advance(d time.Duration)    { c.now.Add(int64(d)) }
func (c *clock) unix(d time.Duration) int64 { return c.Now().Add(d).Unix() }

type jwksServer struct {
	*httptest.Server
	mu     sync.Mutex
	keys   map[string]*rsa.PublicKey
	status int
	body   []byte
	hits   atomic.Int32
	gate   chan struct{}
}

func newJWKSServer(t *testing.T) *jwksServer {
	t.Helper()
	s := &jwksServer{keys: map[string]*rsa.PublicKey{}, status: http.StatusOK}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if r.URL.Path != "/sso/jwks/"+testClientID {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		gate, status, body := s.gate, s.status, s.body
		if body == nil {
			body = jwksDocument(s.keys)
		}
		s.mu.Unlock()
		if gate != nil {
			<-gate
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) set(f func(s *jwksServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func jwksDocument(keys map[string]*rsa.PublicKey) []byte {
	type jwk struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	doc := struct {
		Keys []jwk `json:"keys"`
	}{Keys: []jwk{}}
	for kid, pub := range keys {
		doc.Keys = append(doc.Keys, jwk{
			Kty: "RSA", Kid: kid, Use: "sig", Alg: "RS256",
			N: b64(pub.N.Bytes()), E: b64(big.NewInt(int64(pub.E)).Bytes()),
		})
	}
	out, _ := json.Marshal(doc)
	return out
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func segment(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b64(out)
}

func sign(t testing.TB, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	input := segment(header) + "." + segment(claims)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

func rs256(kid string) map[string]any {
	return map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
}

func validClaims(c *clock) map[string]any {
	return map[string]any{
		"iss":         testIssuer,
		"sub":         "user_01TEST",
		"client_id":   testClientID,
		"sid":         "session_01TEST",
		"org_id":      "org_01TEST",
		"role":        "member",
		"permissions": []string{"posts:read"},
		"email":       "ada@example.com",
		"jti":         "jti_01TEST",
		"auth_time":   c.unix(-time.Hour),
		"iat":         c.unix(-time.Minute),
		"exp":         c.unix(4 * time.Minute),
	}
}

func with(claims map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(claims))
	for k, v := range claims {
		out[k] = v
	}
	if value == nil {
		delete(out, key)
	} else {
		out[key] = value
	}
	return out
}

type fixture struct {
	t      *testing.T
	clock  *clock
	server *jwksServer
	v      *Verifier
}

func newFixture(t *testing.T, mutate ...func(*config.WorkOS)) *fixture {
	t.Helper()
	c := newClock()
	srv := newJWKSServer(t)
	srv.set(func(s *jwksServer) { s.keys["k1"] = &rsaKey(t, 0).PublicKey })
	cfg := config.WorkOS{ClientID: testClientID, BaseURL: srv.URL, Issuers: []string{testIssuer}}
	for _, m := range mutate {
		m(&cfg)
	}
	v, err := New(cfg, WithHTTPClient(srv.Client()), WithClock(c.Now))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &fixture{t: t, clock: c, server: srv, v: v}
}

func (f *fixture) verify(token string) (Identity, error) {
	return f.v.Verify(f.t.Context(), token)
}

func TestVerifyValidToken(t *testing.T) {
	f := newFixture(t)
	id, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock)))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := Identity{
		UserID: "user_01TEST", SessionID: "session_01TEST", OrganizationID: "org_01TEST", Role: "member",
		Permissions: []string{"posts:read"}, Email: "ada@example.com", ExpiresAt: f.clock.Now().Add(4 * time.Minute).Truncate(time.Second),
	}
	if id.Actor != "" || id.Impersonated {
		t.Errorf("Actor = %q, Impersonated = %v without an act claim", id.Actor, id.Impersonated)
	}
	if id.UserID != want.UserID || id.SessionID != want.SessionID || id.OrganizationID != want.OrganizationID ||
		id.Role != want.Role || !slices.Equal(id.Permissions, want.Permissions) || id.Email != want.Email || !id.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
	if !f.v.Configured() {
		t.Error("Configured = false")
	}
}

func TestVerifyRejectsClaims(t *testing.T) {
	f := newFixture(t)
	base := validClaims(f.clock)
	tests := []struct {
		name   string
		claims map[string]any
	}{
		{"expired beyond leeway", with(base, "exp", f.clock.unix(-11*time.Second))},
		{"missing exp", with(base, "exp", nil)},
		{"exp as string", with(base, "exp", "4102444800")},
		{"exp negative", with(base, "exp", -1)},
		{"exp huge", with(base, "exp", 1e300)},
		{"not yet valid", with(base, "nbf", f.clock.unix(time.Minute))},
		{"issued in the future", with(base, "iat", f.clock.unix(time.Minute))},
		{"missing iat", with(base, "iat", nil)},
		{"wrong issuer", with(base, "iss", "https://api.workos.com/user_management/client_other")},
		{"issuer with trailing slash", with(base, "iss", testIssuer+"/")},
		{"missing issuer", with(base, "iss", nil)},
		{"wrong client", with(base, "client_id", "client_other")},
		{"missing client", with(base, "client_id", nil)},
		{"missing sub", with(base, "sub", nil)},
		{"empty sub", with(base, "sub", "")},
		{"missing sid", with(base, "sid", nil)},
		{"agent token", with(base, "sub_profile", "ai_agent")},
		{"widget token", with(base, "sub_type", "authkit")},
		{"null sub_profile", with(base, "sub_profile", json.RawMessage("null"))},
		{"null sub_type", with(base, "sub_type", json.RawMessage("null"))},
		{"empty sub_type", with(base, "sub_type", "")},
		{"missing auth_time", with(base, "auth_time", nil)},
		{"null auth_time", with(base, "auth_time", json.RawMessage("null"))},
		{"auth_time as string", with(base, "auth_time", "1700000000")},
		{"act not an object", with(base, "act", "admin@example.com")},
		{"act an array", with(base, "act", []string{"admin@example.com"})},
		{"null act", with(base, "act", json.RawMessage("null"))},
		{"act without sub", with(base, "act", map[string]any{})},
		{"act with empty sub", with(base, "act", map[string]any{"sub": ""})},
		{"act with null sub", with(base, "act", map[string]any{"sub": nil})},
		{"act with numeric sub", with(base, "act", map[string]any{"sub": 7})},
		{"aud of wrong type", with(base, "aud", 12)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), tt.claims))
			if !errors.Is(err, ErrInvalidToken) {
				t.Errorf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyAcceptsWithinLeeway(t *testing.T) {
	f := newFixture(t)
	base := validClaims(f.clock)
	for name, claims := range map[string]map[string]any{
		"just expired":         with(base, "exp", f.clock.unix(-5*time.Second)),
		"iat slightly ahead":   with(base, "iat", f.clock.unix(5*time.Second)),
		"nbf slightly ahead":   with(base, "nbf", f.clock.unix(5*time.Second)),
		"user profile":         with(base, "sub_profile", "user"),
		"fractional exp":       with(base, "exp", float64(f.clock.unix(time.Minute))+0.5),
		"no email":             with(base, "email", nil),
		"unconfigured aud set": with(base, "aud", "https://other.example.com"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), claims)); err != nil {
				t.Errorf("err = %v, want nil", err)
			}
		})
	}
}

func TestVerifyCarriesTheActor(t *testing.T) {
	f := newFixture(t)
	claims := with(validClaims(f.clock), "act", map[string]any{"sub": "admin@example.com"})
	id, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), claims))
	if err != nil {
		t.Fatal(err)
	}
	if id.Actor != "admin@example.com" || !id.Impersonated || id.UserID != "user_01TEST" {
		t.Errorf("identity = %+v, want actor admin@example.com acting as user_01TEST", id)
	}
}

func TestVerifyAudience(t *testing.T) {
	f := newFixture(t, func(c *config.WorkOS) { c.Audience = "https://api.example.com" })
	base := validClaims(f.clock)
	tests := []struct {
		name string
		aud  any
		ok   bool
	}{
		{"missing", nil, false},
		{"wrong", "https://other.example.com", false},
		{"string", "https://api.example.com", true},
		{"array", []string{"https://other.example.com", "https://api.example.com"}, true},
		{"array without", []string{"https://other.example.com"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := with(base, "aud", tt.aud)
			_, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), claims))
			if tt.ok != (err == nil) {
				t.Errorf("err = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

func TestVerifyAcceptsAnyConfiguredIssuer(t *testing.T) {
	f := newFixture(t, func(c *config.WorkOS) { c.Issuers = []string{"https://api.workos.com", testIssuer} })
	for _, iss := range []string{"https://api.workos.com", testIssuer} {
		claims := with(validClaims(f.clock), "iss", iss)
		if _, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), claims)); err != nil {
			t.Errorf("iss %q: %v", iss, err)
		}
	}
}

func TestVerifyRejectsAlgorithms(t *testing.T) {
	f := newFixture(t)
	claims := segment(validClaims(f.clock))
	pub := rsaKey(t, 0).PublicKey

	hs256 := func() string {
		input := segment(map[string]any{"alg": "HS256", "kid": "k1"}) + "." + claims
		mac := hmac.New(sha256.New, pub.N.Bytes())
		mac.Write([]byte(input))
		return input + "." + b64(mac.Sum(nil))
	}()
	rs512 := func() string {
		input := segment(map[string]any{"alg": "RS512", "kid": "k1"}) + "." + claims
		digest := sha512.Sum512([]byte(input))
		sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey(t, 0), crypto.SHA512, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return input + "." + b64(sig)
	}()
	ps256 := func() string {
		input := segment(map[string]any{"alg": "PS256", "kid": "k1"}) + "." + claims
		digest := sha256.Sum256([]byte(input))
		sig, err := rsa.SignPSS(rand.Reader, rsaKey(t, 0), crypto.SHA256, digest[:], nil)
		if err != nil {
			t.Fatal(err)
		}
		return input + "." + b64(sig)
	}()
	valid := sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))
	validSig := valid[strings.LastIndex(valid, ".")+1:]

	tokens := map[string]string{
		"none without signature": segment(map[string]any{"alg": "none", "kid": "k1"}) + "." + claims + ".",
		"none with signature":    segment(map[string]any{"alg": "none", "kid": "k1"}) + "." + claims + "." + validSig,
		"lowercase rs256":        segment(map[string]any{"alg": "rs256", "kid": "k1"}) + "." + claims + "." + validSig,
		"HS256 with public key":  hs256,
		"RS512":                  rs512,
		"PS256":                  ps256,
	}
	for name, token := range tokens {
		t.Run(name, func(t *testing.T) {
			if _, err := f.verify(token); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	f := newFixture(t)
	claims := validClaims(f.clock)
	valid := sign(t, rsaKey(t, 0), rs256("k1"), claims)
	parts := strings.Split(valid, ".")
	tampered := parts[0] + "." + segment(with(claims, "sub", "user_01OTHER")) + "." + parts[2]
	otherKey := sign(t, rsaKey(t, 1), rs256("k1"), claims)

	tokens := map[string]string{
		"empty":               "",
		"one part":            "abc",
		"two parts":           parts[0] + "." + parts[1],
		"four parts":          valid + ".x",
		"empty payload":       parts[0] + ".." + parts[2],
		"header not base64":   "!!!." + parts[1] + "." + parts[2],
		"padded header":       parts[0] + "=." + parts[1] + "." + parts[2],
		"header not json":     b64([]byte("nope")) + "." + parts[1] + "." + parts[2],
		"header trailing":     b64([]byte(`{"alg":"RS256","kid":"k1"}{}`)) + "." + parts[1] + "." + parts[2],
		"missing kid":         segment(map[string]any{"alg": "RS256"}) + "." + parts[1] + "." + parts[2],
		"crit header":         sign(t, rsaKey(t, 0), map[string]any{"alg": "RS256", "kid": "k1", "crit": []string{"exp"}}, claims),
		"null crit header":    sign(t, rsaKey(t, 0), map[string]any{"alg": "RS256", "kid": "k1", "crit": nil}, claims),
		"empty crit header":   sign(t, rsaKey(t, 0), map[string]any{"alg": "RS256", "kid": "k1", "crit": []string{}}, claims),
		"signature encoding":  parts[0] + "." + parts[1] + ".***",
		"tampered payload":    tampered,
		"signed by other key": otherKey,
		"payload not json":    signRaw(t, rs256("k1"), []byte("nope")),
		"too large":           strings.Repeat("a", maxTokenBytes+1),
	}
	for name, token := range tokens {
		t.Run(name, func(t *testing.T) {
			if _, err := f.verify(token); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func signRaw(t *testing.T, header map[string]any, payload []byte) string {
	t.Helper()
	input := segment(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey(t, 0), crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

func TestUnknownKidIsRejectedAfterOneBoundedRefresh(t *testing.T) {
	f := newFixture(t)
	if _, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))); err != nil {
		t.Fatal(err)
	}
	if hits := f.server.hits.Load(); hits != 1 {
		t.Fatalf("hits after first verify = %d, want 1", hits)
	}

	f.clock.Advance(time.Minute)
	for i := range 20 {
		_, err := f.verify(sign(t, rsaKey(t, 1), rs256("unknown-"+string(rune('a'+i))), validClaims(f.clock)))
		want := ErrUnavailable
		if i == 0 {
			want = ErrInvalidToken
		}
		if !errors.Is(err, want) {
			t.Fatalf("unknown kid %d: err = %v, want %v", i, err, want)
		}
	}
	if hits := f.server.hits.Load(); hits != 2 {
		t.Errorf("hits after a flood of unknown kids = %d, want 2", hits)
	}
}

func TestUnknownKidIsAcceptedAfterRotation(t *testing.T) {
	f := newFixture(t)
	if _, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))); err != nil {
		t.Fatal(err)
	}
	f.server.set(func(s *jwksServer) {
		delete(s.keys, "k1")
		s.keys["k2"] = &rsaKey(t, 1).PublicKey
	})

	f.clock.Advance(5 * time.Second)
	rotated := func() string { return sign(t, rsaKey(t, 1), rs256("k2"), validClaims(f.clock)) }
	if _, err := f.verify(rotated()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("within the refresh interval: err = %v, want ErrUnavailable", err)
	}

	f.clock.Advance(jwksMinRefresh)
	if _, err := f.verify(rotated()); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	retired := func() string { return sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock)) }
	if _, err := f.verify(retired()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("retired key within the refresh interval: err = %v, want ErrUnavailable", err)
	}
	f.clock.Advance(jwksMinRefresh)
	if _, err := f.verify(retired()); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("retired key after a fresh fetch: err = %v, want ErrInvalidToken", err)
	}
}

func TestJWKSDownWithoutCache(t *testing.T) {
	f := newFixture(t)
	f.server.set(func(s *jwksServer) { s.status = http.StatusServiceUnavailable })

	token := sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))
	if _, err := f.verify(token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, err := f.verify(token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("second err = %v, want ErrUnavailable", err)
	}
	if hits := f.server.hits.Load(); hits != 1 {
		t.Errorf("hits = %d, want 1 within the refresh interval", hits)
	}

	f.server.set(func(s *jwksServer) { s.status = http.StatusOK })
	f.clock.Advance(jwksMinRefresh)
	if _, err := f.verify(token); err != nil {
		t.Errorf("after recovery: %v", err)
	}
}

func TestJWKSDownWithCachedKey(t *testing.T) {
	f := newFixture(t)
	if _, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))); err != nil {
		t.Fatal(err)
	}
	f.server.set(func(s *jwksServer) { s.status = http.StatusInternalServerError })

	f.clock.Advance(2 * jwksTTL)
	if _, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))); err != nil {
		t.Fatalf("stale key while JWKS is down: %v", err)
	}
	waitForRefresh(t, f.v.keys)
	if _, err := f.verify(sign(t, rsaKey(t, 1), rs256("k9"), validClaims(f.clock))); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unknown kid while JWKS is down: err = %v, want ErrUnavailable", err)
	}

	f.clock.Advance(jwksMaxStale)
	if _, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))); !errors.Is(err, ErrUnavailable) {
		t.Errorf("key past max staleness: err = %v, want ErrUnavailable", err)
	}
}

func TestStaleKeyIsRefreshedInBackground(t *testing.T) {
	f := newFixture(t)
	token := func() string { return sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock)) }
	if _, err := f.verify(token()); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(jwksTTL)
	if _, err := f.verify(token()); err != nil {
		t.Fatal(err)
	}
	waitForRefresh(t, f.v.keys)
	if hits := f.server.hits.Load(); hits != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
	if _, err := f.verify(token()); err != nil {
		t.Fatal(err)
	}
	if hits := f.server.hits.Load(); hits != 2 {
		t.Errorf("hits after refresh = %d, want 2", hits)
	}
}

func waitForRefresh(t *testing.T, j *jwks) {
	t.Helper()
	j.mu.Lock()
	done := j.inflight
	j.mu.Unlock()
	if done != nil {
		<-done
	}
}

func TestConcurrentVerifiesShareOneFetch(t *testing.T) {
	f := newFixture(t)
	gate := make(chan struct{})
	f.server.set(func(s *jwksServer) { s.gate = gate })
	token := sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))

	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Go(func() {
			_, err := f.verify(token)
			errs <- err
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Verify: %v", err)
		}
	}
	if hits := f.server.hits.Load(); hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
}

func TestVerifyHonoursContextWhileWaiting(t *testing.T) {
	f := newFixture(t)
	gate := make(chan struct{})
	f.server.set(func(s *jwksServer) { s.gate = gate })
	t.Cleanup(func() { close(gate) })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.v.Verify(ctx, sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock)))
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want ErrUnavailable and context.Canceled", err)
	}
}

func TestJWKSFetchFailures(t *testing.T) {
	smallModulus := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 1023), big.NewInt(1))
	k := &rsaKey(t, 0).PublicKey
	n, e := b64(k.N.Bytes()), b64(big.NewInt(int64(k.E)).Bytes())
	tests := map[string]struct {
		status int
		body   string
	}{
		"not found":          {http.StatusNotFound, `{}`},
		"redirect":           {http.StatusFound, `{}`},
		"not json":           {http.StatusOK, `nope`},
		"too large":          {http.StatusOK, `{"keys":[],"pad":"` + strings.Repeat("a", jwksMaxBytes) + `"}`},
		"no keys":            {http.StatusOK, `{"keys":[]}`},
		"small modulus":      {http.StatusOK, `{"keys":[{"kty":"RSA","kid":"k1","n":"` + b64(smallModulus.Bytes()) + `","e":"AQAB"}]}`},
		"ec key":             {http.StatusOK, `{"keys":[{"kty":"EC","kid":"k1","crv":"P-256","x":"AA","y":"AA"}]}`},
		"encryption key":     {http.StatusOK, `{"keys":[{"kty":"RSA","kid":"k1","use":"enc","n":"` + n + `","e":"` + e + `"}]}`},
		"other algorithm":    {http.StatusOK, `{"keys":[{"kty":"RSA","kid":"k1","alg":"RS512","n":"` + n + `","e":"` + e + `"}]}`},
		"missing kid":        {http.StatusOK, `{"keys":[{"kty":"RSA","n":"` + n + `","e":"` + e + `"}]}`},
		"bad modulus":        {http.StatusOK, `{"keys":[{"kty":"RSA","kid":"k1","n":"***","e":"` + e + `"}]}`},
		"bad exponent":       {http.StatusOK, `{"keys":[{"kty":"RSA","kid":"k1","n":"` + n + `","e":"***"}]}`},
		"even exponent":      {http.StatusOK, `{"keys":[{"kty":"RSA","kid":"k1","n":"` + n + `","e":"` + b64([]byte{4}) + `"}]}`},
		"exponent too large": {http.StatusOK, `{"keys":[{"kty":"RSA","kid":"k1","n":"` + n + `","e":"` + b64([]byte{1, 0, 0, 0, 1}) + `"}]}`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.server.set(func(s *jwksServer) { s.status, s.body = tt.status, []byte(tt.body) })
			_, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock)))
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("err = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestJWKSKeepsFirstOfDuplicateKids(t *testing.T) {
	k0, k1 := &rsaKey(t, 0).PublicKey, &rsaKey(t, 1).PublicKey
	body := `{"keys":[` +
		`{"kty":"RSA","kid":"k1","n":"` + b64(k0.N.Bytes()) + `","e":"AQAB"},` +
		`{"kty":"RSA","kid":"k1","n":"` + b64(k1.N.Bytes()) + `","e":"AQAB"}]}`
	f := newFixture(t)
	f.server.set(func(s *jwksServer) { s.body = []byte(body) })
	if _, err := f.verify(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))); err != nil {
		t.Errorf("first key: %v", err)
	}
	if _, err := f.verify(sign(t, rsaKey(t, 1), rs256("k1"), validClaims(f.clock))); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("duplicate key: err = %v, want ErrInvalidToken", err)
	}
}

func TestJWKSUnreachable(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	client := srv.Client()
	url := srv.URL
	srv.Close()
	v, err := New(config.WorkOS{ClientID: testClientID, BaseURL: url, Issuers: []string{testIssuer}}, WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Verify(t.Context(), sign(t, rsaKey(t, 0), rs256("k1"), validClaims(newClock())))
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestUnconfigured(t *testing.T) {
	v, err := New(config.WorkOS{BaseURL: "https://api.workos.com"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Configured() {
		t.Error("Configured = true without a client id")
	}
	if _, err := v.Verify(t.Context(), "anything"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
	var nilVerifier *Verifier
	if nilVerifier.Configured() {
		t.Error("nil verifier is configured")
	}
}

func TestNewRejectsUnsafeConfig(t *testing.T) {
	for name, cfg := range map[string]config.WorkOS{
		"plain http": {ClientID: testClientID, BaseURL: "http://api.workos.com", Issuers: []string{testIssuer}},
		"no issuers": {ClientID: testClientID, BaseURL: "https://api.workos.com"},
		"bad url":    {ClientID: testClientID, BaseURL: "https://[::1", Issuers: []string{testIssuer}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Error("New succeeded")
			}
		})
	}
}

func TestDefaultClientIsUsedWithoutOption(t *testing.T) {
	v, err := New(config.WorkOS{ClientID: testClientID, BaseURL: "https://api.workos.com", Issuers: []string{testIssuer}})
	if err != nil {
		t.Fatal(err)
	}
	if v.keys.client.Timeout != jwksFetchTimeout || v.keys.client.CheckRedirect == nil {
		t.Errorf("client = %+v", v.keys.client)
	}
	if v.keys.url != "https://api.workos.com/sso/jwks/"+testClientID {
		t.Errorf("url = %q", v.keys.url)
	}
}

func TestJWKSTruncatedBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"keys":[`))
	}))
	t.Cleanup(srv.Close)
	v, err := New(config.WorkOS{ClientID: testClientID, BaseURL: srv.URL, Issuers: []string{testIssuer}}, WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Verify(t.Context(), sign(t, rsaKey(t, 0), rs256("k1"), validClaims(newClock())))
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "jwks read") {
		t.Errorf("err = %v, want an ErrUnavailable read error", err)
	}
}

func TestJWKSRefreshFailureIsLoggedOncePerAttempt(t *testing.T) {
	var logged atomic.Int32
	logger := slog.New(countingHandler{&logged})
	f := newFixture(t)
	f.server.set(func(s *jwksServer) { s.status = http.StatusBadGateway })
	v, err := New(config.WorkOS{ClientID: testClientID, BaseURL: f.server.URL, Issuers: []string{testIssuer}},
		WithHTTPClient(f.server.Client()), WithClock(f.clock.Now), WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if _, err := v.Verify(t.Context(), sign(t, rsaKey(t, 0), rs256("k1"), validClaims(f.clock))); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable", err)
		}
	}
	if n := logged.Load(); n != 1 {
		t.Errorf("log lines = %d, want 1", n)
	}
}

type countingHandler struct{ n *atomic.Int32 }

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(context.Context, slog.Record) error {
	h.n.Add(1)
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }
