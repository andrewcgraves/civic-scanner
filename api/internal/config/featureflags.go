package config

import (
	"os"
	"strings"
)

type FeatureFlags struct {
	PostHogKey  string
	PostHogHost string
	On          []string
}

func loadFeatureFlags() FeatureFlags {
	return FeatureFlags{
		PostHogKey:  os.Getenv("POSTHOG_API_KEY"),
		PostHogHost: os.Getenv("POSTHOG_HOST"),
		On:          splitList(os.Getenv("FEATURE_FLAGS")),
	}
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
