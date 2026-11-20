package config

import (
	"net/url"
	"os"
	"strings"
	"unicode"
)

type Stripe struct {
	SecretKey Secret
	PriceID   string
	Problem   string
}

func (s Stripe) Configured() bool { return s.SecretKey.IsSet() && s.Problem == "" }

func (s Stripe) LiveMode() bool { return strings.Contains(s.SecretKey.Reveal(), "_live_") }

func (s Stripe) Mode() string {
	if s.LiveMode() {
		return "live"
	}
	return "test"
}

func loadStripe(publicWebURL string) Stripe {
	key := strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
	price := strings.TrimSpace(os.Getenv("STRIPE_PRICE_ID"))
	if problem := stripeProblem(key, price, publicWebURL); problem != "" {
		return Stripe{Problem: problem}
	}
	return Stripe{SecretKey: NewSecret(key), PriceID: price}
}

func stripeProblem(key, price, publicWebURL string) string {
	switch {
	case key == "" && price == "":
		return ""
	case key == "":
		return "STRIPE_PRICE_ID is set without STRIPE_SECRET_KEY"
	case price == "":
		return "STRIPE_SECRET_KEY is set without STRIPE_PRICE_ID"
	case strings.HasPrefix(key, "pk_"):
		return "STRIPE_SECRET_KEY is a publishable key (pk_); use a secret (sk_) or restricted (rk_) key"
	case !strings.HasPrefix(key, "sk_") && !strings.HasPrefix(key, "rk_"), len(key) <= 3, hasSpace(key):
		return "STRIPE_SECRET_KEY must start with sk_ or rk_ and contain no whitespace"
	case !strings.HasPrefix(price, "price_") || len(price) <= len("price_") || hasSpace(price):
		return "STRIPE_PRICE_ID must be a Stripe price id (price_...) without whitespace"
	case publicWebURL == "":
		return "PUBLIC_WEB_URL is not set; the checkout and billing portal return links are built from it"
	case strings.Contains(key, "_live_") && !isHTTPS(publicWebURL):
		return "PUBLIC_WEB_URL must be an https URL when STRIPE_SECRET_KEY is a live key"
	}
	return ""
}

func hasSpace(s string) bool { return strings.IndexFunc(s, unicode.IsSpace) >= 0 }

func isHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https"
}
