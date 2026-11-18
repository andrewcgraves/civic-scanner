package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type FeatureFlags struct {
	PostHogKey     string
	PostHogHost    string
	PostHogTimeout time.Duration
	On             []string
}

func loadFeatureFlags() (FeatureFlags, error) {
	timeout, err := time.ParseDuration(envOr("POSTHOG_FLAG_TIMEOUT", "1s"))
	if err == nil && timeout <= 0 {
		err = errors.New("must be positive")
	}
	if err != nil {
		return FeatureFlags{}, fmt.Errorf("POSTHOG_FLAG_TIMEOUT: %w", err)
	}
	return FeatureFlags{
		PostHogKey:     os.Getenv("POSTHOG_API_KEY"),
		PostHogHost:    os.Getenv("POSTHOG_HOST"),
		PostHogTimeout: timeout,
		On:             splitList(os.Getenv("FEATURE_FLAGS")),
	}, nil
}

func splitList(s string) []string {
	var items []string
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
