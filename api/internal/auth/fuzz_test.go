package auth

import (
	"crypto/rsa"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func FuzzVerify(f *testing.F) {
	t := f
	c := newClock()
	f.Add(sign(t, rsaKey(t, 0), rs256("k1"), validClaims(c)))
	f.Add(sign(t, rsaKey(t, 0), map[string]any{"alg": "RS256", "kid": "k1", "crit": nil}, validClaims(c)))
	f.Add(sign(t, rsaKey(t, 0), rs256("k1"), with(validClaims(c), "exp", "1")))
	f.Add(segment(map[string]any{"alg": "none", "kid": "k1"}) + "." + segment(validClaims(c)) + ".")
	f.Add("a.b.c")
	f.Add("..")
	f.Add("")

	keys := newJWKS("https://127.0.0.1:1/sso/jwks/"+testClientID, &http.Client{Timeout: time.Second}, c.Now, slog.New(slog.DiscardHandler))
	keys.set = &keySet{keys: map[string]*rsa.PublicKey{"k1": &rsaKey(t, 0).PublicKey}, fetchedAt: c.Now()}
	v := &Verifier{clientID: testClientID, issuers: []string{testIssuer}, keys: keys, now: c.Now}
	f.Fuzz(func(t *testing.T, token string) {
		id, err := v.Verify(t.Context(), token)
		switch {
		case err == nil:
			if id.UserID == "" || id.SessionID == "" || !id.ExpiresAt.After(c.Now().Add(-clockLeeway-time.Second)) {
				t.Errorf("accepted token with identity %+v", id)
			}
		case !errors.Is(err, ErrInvalidToken) && !errors.Is(err, ErrUnavailable):
			t.Errorf("unexpected error class: %v", err)
		}
	})
}

func FuzzParseJWKS(f *testing.F) {
	k := &rsaKey(f, 0).PublicKey
	f.Add(jwksDocument(map[string]*rsa.PublicKey{"k1": k}))
	f.Add([]byte(`{"keys":[]}`))
	f.Add([]byte(`{"keys":[{"kty":"RSA","kid":"k1","n":"AQAB","e":"AQAB"}]}`))
	f.Add([]byte(`{"keys":[{"kty":"RSA","kid":"k1","n":"","e":""}]}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, body []byte) {
		keys, err := parseJWKS(body)
		if err != nil {
			return
		}
		if len(keys) == 0 {
			t.Fatal("no error and no keys")
		}
		for kid, key := range keys {
			if kid == "" || key.N.BitLen() < minRSAModulusBits || key.E < 3 || key.E%2 == 0 {
				t.Errorf("accepted weak key %q", kid)
			}
		}
	})
}
