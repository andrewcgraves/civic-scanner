package webhooks

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

func testSealer(t *testing.T, fill byte) *Sealer {
	t.Helper()
	s, err := NewSealer(config.NewSecret(string(bytes.Repeat([]byte{fill}, config.WebhookSecretKeyBytes))))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSecretIsRandomAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		secret := NewSecret().Reveal()
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, SecretPrefix))
		if !strings.HasPrefix(secret, SecretPrefix) || err != nil || len(raw) != secretBytes {
			t.Fatalf("secret %q: want %s and %d base64 bytes (%v)", secret, SecretPrefix, secretBytes, err)
		}
		if seen[secret] {
			t.Fatal("a secret repeated")
		}
		seen[secret] = true
	}
}

func TestNewSecretNeverPrints(t *testing.T) {
	secret := NewSecret()
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "secret", secret)
	for _, out := range []string{fmt.Sprintf("%v %s %+v %#v", secret, secret, secret, secret), logs.String()} {
		if strings.Contains(out, secret.Reveal()) || strings.Contains(out, SecretPrefix) {
			t.Errorf("the secret was printed: %s", out)
		}
	}
}

func TestSealOpensOnlyForTheSameSubscriptionAndKey(t *testing.T) {
	s := testSealer(t, 1)
	id := uuid.New()
	secret := NewSecret()
	sealed, err := s.Seal(id, secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(secret.Reveal())) || bytes.Contains(sealed, []byte(strings.TrimPrefix(secret.Reveal(), SecretPrefix))) {
		t.Fatal("the sealed value contains the secret")
	}
	opened, err := s.Open(id, sealed)
	if err != nil || opened.Reveal() != secret.Reveal() {
		t.Fatalf("Open = %v, %v", opened, err)
	}
	again, err := s.Seal(id, secret)
	if err != nil || bytes.Equal(again, sealed) {
		t.Errorf("sealing twice gave the same bytes (%v): the nonce is not random", err)
	}

	if _, err := s.Open(uuid.New(), sealed); !errors.Is(err, ErrUnreadable) {
		t.Errorf("another subscription's id: err = %v, want ErrUnreadable", err)
	}
	if _, err := testSealer(t, 2).Open(id, sealed); !errors.Is(err, ErrUnreadable) {
		t.Errorf("another key: err = %v, want ErrUnreadable", err)
	}
	for name, bad := range map[string][]byte{
		"empty":       nil,
		"short":       sealed[:10],
		"version":     append([]byte{9}, sealed[1:]...),
		"tampered":    append(append([]byte{}, sealed[:len(sealed)-1]...), sealed[len(sealed)-1]^1),
		"truncated":   sealed[:len(sealed)-1],
		"only header": sealed[:1+12],
	} {
		if _, err := s.Open(id, bad); !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: err = %v, want ErrUnreadable", name, err)
		}
	}
}

func TestSealerNeedsAKey(t *testing.T) {
	for _, key := range []string{"", "short", strings.Repeat("k", 33)} {
		if s, err := NewSealer(config.NewSecret(key)); !errors.Is(err, ErrNotConfigured) || s.Configured() {
			t.Errorf("key of %d bytes: err = %v", len(key), err)
		}
	}
	var s *Sealer
	if _, err := s.Seal(uuid.New(), NewSecret()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil Seal: %v", err)
	}
	if _, err := s.Open(uuid.New(), []byte{1}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("nil Open: %v", err)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	for raw, want := range map[string]string{
		"https://example.com/hooks":                        "https://example.com/hooks",
		"  HTTPS://Hooks.Example.COM/In?A=1&b=B  ":         "https://hooks.example.com/In?A=1&b=B",
		"https://example.com":                              "https://example.com",
		"https://example.com:8443/x":                       "https://example.com:8443/x",
		"https://[2001:db8::1]/x":                          "https://[2001:db8::1]/x",
		"https://xn--mnchen-3ya.de/hook":                   "https://xn--mnchen-3ya.de/hook",
		"https://example.com/a%20b":                        "https://example.com/a%20b",
		"https://example.com/" + strings.Repeat("a", 2000): "https://example.com/" + strings.Repeat("a", 2000),
	} {
		got, problem := NormalizeEndpoint(raw)
		if problem != nil || got != want {
			t.Errorf("NormalizeEndpoint(%q) = %q, %v; want %q", raw, got, problem, want)
		}
	}
}

func TestNormalizeEndpointRefuses(t *testing.T) {
	for raw, code := range map[string]string{
		"":    "required",
		"   ": "required",
		"https://example.com/" + strings.Repeat("a", MaxEndpointBytes): "too_long",
		"https://example.com/a b":                                      "invalid_url",
		"https://example.com/\x00":                                     "invalid_url",
		"https://example.com/\xff":                                     "invalid_url",
		"https://example.com/a\u00a0b":                                 "invalid_url",
		"example.com/hooks":                                            "invalid_url",
		"/hooks":                                                       "invalid_url",
		"https://":                                                     "invalid_url",
		"https:///hooks":                                               "invalid_url",
		"https:example.com":                                            "invalid_url",
		"https://:443/x":                                               "invalid_url",
		"https://example.com:0/x":                                      "invalid_url",
		"https://example.com:70000/x":                                  "invalid_url",
		"https://example.com:abc/x":                                    "invalid_url",
		"http://example.com/hooks":                                     "unsupported_scheme",
		"ftp://example.com/hooks":                                      "unsupported_scheme",
		"javascript:alert(1)":                                          "unsupported_scheme",
		"https://user:pass@example.com/":                               "credentials",
		"https://user@example.com/":                                    "credentials",
		"https://example.com/hooks#top":                                "fragment",
		"https://example.com/hooks#":                                   "fragment",
	} {
		got, problem := NormalizeEndpoint(raw)
		if problem == nil || problem.Code != code || got != "" {
			t.Errorf("NormalizeEndpoint(%q) = %q, %+v; want code %s", raw, got, problem, code)
			continue
		}
		if problem.Error() == "" {
			t.Errorf("%q: message %q", raw, problem.Error())
		}
	}
}
