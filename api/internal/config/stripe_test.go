package config

import (
	"fmt"
	"strings"
	"testing"
)

const (
	testStripeKey   = "sk_test_FAKEFAKEFAKE"
	testStripePrice = "price_FAKE123"
	testLiveKey     = "sk_live_FAKEFAKEFAKE" //nolint:gosec
)

func setStripeEnv(t *testing.T, key, price, webURL string) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("STRIPE_SECRET_KEY", key)
	t.Setenv("STRIPE_PRICE_ID", price)
	t.Setenv("PUBLIC_WEB_URL", webURL)
}

func TestLoadStripeUnsetIsNotConfigured(t *testing.T) {
	setStripeEnv(t, "", "", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Stripe.Configured() || cfg.Stripe != (Stripe{}) {
		t.Errorf("Stripe = %#v, want not configured", cfg.Stripe)
	}
	if got := cfg.Stripe.String(); got != "Stripe{not configured}" {
		t.Errorf("String() = %q", got)
	}
}

func TestLoadStripe(t *testing.T) {
	for key, live := range map[string]bool{
		" sk_test_FAKEFAKEFAKE ": false,
		"rk_test_FAKEFAKEFAKE":   false,
		"sk_live_FAKEFAKEFAKE":   true,
		"rk_live_FAKEFAKEFAKE":   true,
	} {
		setStripeEnv(t, key, " "+testStripePrice+" ", "https://civic.example")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("STRIPE_SECRET_KEY=%q: %v", key, err)
		}
		s := cfg.Stripe
		if !s.Configured() || s.SecretKey != strings.TrimSpace(key) || s.PriceID != testStripePrice || s.LiveMode() != live {
			t.Errorf("STRIPE_SECRET_KEY=%q: Stripe = %+v, live %v", key, s.PriceID, s.LiveMode())
		}
	}
}

func TestStripeNeverPrintsTheKey(t *testing.T) {
	for _, s := range []Stripe{
		{SecretKey: testStripeKey, PriceID: testStripePrice},
		{SecretKey: testLiveKey, PriceID: testStripePrice},
	} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			got := fmt.Sprintf(format, s)
			if strings.Contains(got, "FAKEFAKE") {
				t.Errorf("%s printed the key: %s", format, got)
			}
			if !strings.Contains(got, testStripePrice) {
				t.Errorf("%s = %q, want the price id", format, got)
			}
		}
		got := fmt.Sprintf("%+v", Config{Stripe: s})
		if strings.Contains(got, "FAKEFAKE") {
			t.Errorf("Config printed the key: %s", got)
		}
	}
	if got := (Stripe{SecretKey: testLiveKey, PriceID: testStripePrice}).String(); !strings.Contains(got, "live mode") {
		t.Errorf("String() = %q, want live mode", got)
	}
}

func TestLoadStripeErrors(t *testing.T) {
	for name, tc := range map[string]struct{ key, price, webURL, want string }{
		"price without key":   {"", testStripePrice, "https://civic.example", "STRIPE_PRICE_ID is set without STRIPE_SECRET_KEY"},
		"key without price":   {testStripeKey, "", "https://civic.example", "STRIPE_SECRET_KEY is set without STRIPE_PRICE_ID"},
		"publishable key":     {"pk_test_FAKEFAKEFAKE", testStripePrice, "https://civic.example", "publishable key"},
		"no mode":             {"sk_FAKEFAKEFAKE", testStripePrice, "https://civic.example", "STRIPE_SECRET_KEY: must be"},
		"unknown prefix":      {"xk_test_FAKEFAKEFAKE", testStripePrice, "https://civic.example", "STRIPE_SECRET_KEY: must be"},
		"inner space":         {"sk_test_FAKE FAKE", testStripePrice, "https://civic.example", "STRIPE_SECRET_KEY: must be"},
		"webhook secret":      {"whsec_FAKEFAKEFAKE", testStripePrice, "https://civic.example", "STRIPE_SECRET_KEY: must be"},
		"product not price":   {testStripeKey, "prod_FAKE123", "https://civic.example", "STRIPE_PRICE_ID: must be"},
		"lookup key":          {testStripeKey, "civic-monthly", "https://civic.example", "STRIPE_PRICE_ID: must be"},
		"empty price suffix":  {testStripeKey, "price_", "https://civic.example", "STRIPE_PRICE_ID: must be"},
		"without web url":     {testStripeKey, testStripePrice, "", "needs PUBLIC_WEB_URL"},
		"with a bad web url":  {testStripeKey, testStripePrice, "civic.example", "PUBLIC_WEB_URL"},
		"key in key position": {"sk_test_", testStripePrice, "https://civic.example", "STRIPE_SECRET_KEY: must be"},
	} {
		setStripeEnv(t, tc.key, tc.price, tc.webURL)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), "FAKEFAKE") {
			t.Errorf("%s: the error shows the key: %v", name, err)
		}
	}
}
