package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	stripeSecretKeyPattern = regexp.MustCompile(`^(sk|rk)_(test|live)_[A-Za-z0-9]+$`)
	stripePriceIDPattern   = regexp.MustCompile(`^price_[A-Za-z0-9]+$`)
)

type Stripe struct {
	SecretKey string
	PriceID   string
}

func (s Stripe) Configured() bool { return s.SecretKey != "" }

func (s Stripe) LiveMode() bool {
	return strings.HasPrefix(s.SecretKey, "sk_live_") || strings.HasPrefix(s.SecretKey, "rk_live_")
}

func (s Stripe) String() string {
	if !s.Configured() {
		return "Stripe{not configured}"
	}
	mode := "test"
	if s.LiveMode() {
		mode = "live"
	}
	return fmt.Sprintf("Stripe{%s mode, price %s}", mode, s.PriceID)
}

func (s Stripe) GoString() string { return s.String() }

func loadStripe() (Stripe, error) {
	s := Stripe{
		SecretKey: strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")),
		PriceID:   strings.TrimSpace(os.Getenv("STRIPE_PRICE_ID")),
	}
	switch {
	case s.SecretKey == "" && s.PriceID == "":
		return Stripe{}, nil
	case s.SecretKey == "":
		return Stripe{}, errors.New("STRIPE_PRICE_ID is set without STRIPE_SECRET_KEY: set both, or neither to turn billing off")
	case s.PriceID == "":
		return Stripe{}, errors.New("STRIPE_SECRET_KEY is set without STRIPE_PRICE_ID: set both, or neither to turn billing off")
	case strings.HasPrefix(s.SecretKey, "pk_"):
		return Stripe{}, errors.New("STRIPE_SECRET_KEY: is a publishable key; use a secret (sk_) or restricted (rk_) key")
	case !stripeSecretKeyPattern.MatchString(s.SecretKey):
		return Stripe{}, errors.New("STRIPE_SECRET_KEY: must be a Stripe secret or restricted key, sk_ or rk_ followed by test_ or live_")
	case !stripePriceIDPattern.MatchString(s.PriceID):
		return Stripe{}, errors.New("STRIPE_PRICE_ID: must be a Stripe price id such as price_1Abc")
	}
	return s, nil
}
