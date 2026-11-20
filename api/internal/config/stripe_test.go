package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const (
	testStripeKey   = "sk_test_FAKEFAKEFAKE" //nolint:gosec
	testLiveKey     = "sk_live_FAKEFAKEFAKE" //nolint:gosec
	testStripePrice = "price_FAKE123"
	fakeMarker      = "FAKEFAKE"
)

func setStripeEnv(t *testing.T, key, price, webURL string) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("STRIPE_SECRET_KEY", key)
	t.Setenv("STRIPE_PRICE_ID", price)
	t.Setenv("PUBLIC_WEB_URL", webURL)
}

func TestLoadStripeUnsetIsNotConfiguredWithoutAProblem(t *testing.T) {
	setStripeEnv(t, "", "", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Stripe.Configured() || cfg.Stripe != (Stripe{}) {
		t.Errorf("Stripe = %+v, want not configured and no problem", cfg.Stripe)
	}
}

func TestLoadStripe(t *testing.T) {
	for key, want := range map[string]struct {
		mode   string
		webURL string
	}{
		" sk_test_FAKEFAKEFAKE ":      {"test", "http://localhost:3000"},
		"rk_test_FAKEFAKEFAKE":        {"test", "https://civic.example"},
		"sk_org_test_FAKEFAKEFAKE":    {"test", "https://civic.example"},
		"sk_live_FAKEFAKEFAKE":        {"live", "https://civic.example"},
		"rk_live_FAKEFAKEFAKE":        {"live", "https://civic.example/app"},
		"sk_org_live_FAKEFAKEFAKE":    {"live", "https://civic.example"},
		"sk_FAKEFAKEFAKE_new_format1": {"test", "https://civic.example"},
	} {
		setStripeEnv(t, key, " "+testStripePrice+" ", want.webURL)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("STRIPE_SECRET_KEY=%q: %v", key, err)
		}
		s := cfg.Stripe
		if !s.Configured() || s.Problem != "" || s.SecretKey.Reveal() != strings.TrimSpace(key) || s.PriceID != testStripePrice || s.Mode() != want.mode {
			t.Errorf("STRIPE_SECRET_KEY=%q: configured %v, problem %q, price %q, mode %s", key, s.Configured(), s.Problem, s.PriceID, s.Mode())
		}
	}
}

func TestLoadStripeProblemsTurnBillingOffWithoutFailing(t *testing.T) {
	for name, tc := range map[string]struct{ key, price, webURL, want string }{
		"price without key":        {"", testStripePrice, "https://civic.example", "STRIPE_PRICE_ID is set without STRIPE_SECRET_KEY"},
		"key without price":        {testStripeKey, "", "https://civic.example", "STRIPE_SECRET_KEY is set without STRIPE_PRICE_ID"},
		"publishable key":          {"pk_test_FAKEFAKEFAKE", testStripePrice, "https://civic.example", "publishable key"},
		"publishable live key":     {"pk_live_FAKEFAKEFAKE", testStripePrice, "https://civic.example", "publishable key"},
		"unknown prefix":           {"xk_test_FAKEFAKEFAKE", testStripePrice, "https://civic.example", "STRIPE_SECRET_KEY must start with sk_ or rk_"},
		"webhook secret":           {"whsec_FAKEFAKEFAKE", testStripePrice, "https://civic.example", "STRIPE_SECRET_KEY must start with sk_ or rk_"},
		"prefix only":              {"sk_", testStripePrice, "https://civic.example", "STRIPE_SECRET_KEY must start with sk_ or rk_"},
		"inner space":              {"sk_test_FAKE FAKE", testStripePrice, "https://civic.example", "no whitespace"},
		"inner tab":                {"sk_test_FAKE\tFAKE", testStripePrice, "https://civic.example", "no whitespace"},
		"product not price":        {testStripeKey, "prod_FAKE123", "https://civic.example", "STRIPE_PRICE_ID must be"},
		"lookup key":               {testStripeKey, "civic-monthly", "https://civic.example", "STRIPE_PRICE_ID must be"},
		"empty price suffix":       {testStripeKey, "price_", "https://civic.example", "STRIPE_PRICE_ID must be"},
		"price with space":         {testStripeKey, "price_FAKE 1", "https://civic.example", "STRIPE_PRICE_ID must be"},
		"without web url":          {testStripeKey, testStripePrice, "", "PUBLIC_WEB_URL is not set"},
		"live key over http":       {testLiveKey, testStripePrice, "http://civic.example", "PUBLIC_WEB_URL must be an https URL"},
		"live restricted key http": {"rk_live_FAKEFAKEFAKE", testStripePrice, "http://localhost:3000", "PUBLIC_WEB_URL must be an https URL"},
	} {
		setStripeEnv(t, tc.key, tc.price, tc.webURL)
		cfg, err := Load()
		if err != nil {
			t.Errorf("%s: Load failed: %v", name, err)
			continue
		}
		s := cfg.Stripe
		if s.Configured() || s.SecretKey.IsSet() || s.PriceID != "" {
			t.Errorf("%s: Stripe = configured %v, key set %v, price %q; want billing off", name, s.Configured(), s.SecretKey.IsSet(), s.PriceID)
		}
		if !strings.Contains(s.Problem, tc.want) {
			t.Errorf("%s: problem = %q, want it to contain %q", name, s.Problem, tc.want)
		}
		if strings.Contains(s.Problem, fakeMarker) || strings.Contains(s.Problem, "FAKE123") {
			t.Errorf("%s: the problem shows a value: %q", name, s.Problem)
		}
	}
}

func TestStripeOnlyAffectsBillingWhenPublicWebURLIsMalformed(t *testing.T) {
	setStripeEnv(t, testStripeKey, testStripePrice, "civic.example")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PUBLIC_WEB_URL") {
		t.Errorf("err = %v, want the existing PUBLIC_WEB_URL error", err)
	}
}

func TestSecretsNeverPrint(t *testing.T) {
	cfg := Config{
		DatabaseURL: "postgres://localhost/test",
		Stripe:      Stripe{SecretKey: NewSecret(testLiveKey), PriceID: testStripePrice},
	}
	var outputs []string
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		outputs = append(outputs, fmt.Sprintf(format, cfg), fmt.Sprintf(format, cfg.Stripe), fmt.Sprintf(format, cfg.Stripe.SecretKey))
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(encoded))
	text, err := cfg.Stripe.SecretKey.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(text))

	for name, handler := range map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	} {
		var buf bytes.Buffer
		logger := slog.New(handler(&buf))
		logger.Info("config", "config", cfg, "stripe", cfg.Stripe, "key", cfg.Stripe.SecretKey)
		logger.Info("grouped", slog.Group("billing", "key", cfg.Stripe.SecretKey))
		outputs = append(outputs, name+": "+buf.String())
	}

	for _, out := range outputs {
		if strings.Contains(out, fakeMarker) {
			t.Errorf("printed the key: %s", out)
		}
	}
	if !strings.Contains(string(encoded), `"SecretKey":"[redacted]"`) || !strings.Contains(string(encoded), testStripePrice) {
		t.Errorf("json = %s, want a redacted key and the price", encoded)
	}
	if got := fmt.Sprintf("%#v", cfg.Stripe.SecretKey); got != `config.Secret("[redacted]")` {
		t.Errorf("%%#v = %s", got)
	}
	if got := fmt.Sprint(Secret{}); got != "" {
		t.Errorf("an empty secret prints %q, want nothing", got)
	}
	if cfg.Stripe.SecretKey.Reveal() != testLiveKey {
		t.Error("Reveal lost the key")
	}
}
