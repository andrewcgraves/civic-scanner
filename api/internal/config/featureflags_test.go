package config

import (
	"slices"
	"testing"
)

func TestLoadFeatureFlags(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("FEATURE_FLAGS", " new-search, ,digest-emails ,")
	t.Setenv("POSTHOG_API_KEY", "phc_test")
	t.Setenv("POSTHOG_HOST", "https://eu.i.posthog.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"new-search", "digest-emails"}; !slices.Equal(cfg.FeatureFlags.On, want) {
		t.Errorf("On = %q, want %q", cfg.FeatureFlags.On, want)
	}
	if cfg.FeatureFlags.PostHogKey != "phc_test" || cfg.FeatureFlags.PostHogHost != "https://eu.i.posthog.com" {
		t.Errorf("unexpected PostHog config: %+v", cfg.FeatureFlags)
	}
}

func TestLoadFeatureFlagsUnset(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("FEATURE_FLAGS", "")
	t.Setenv("POSTHOG_API_KEY", "")
	t.Setenv("POSTHOG_HOST", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.FeatureFlags.On) != 0 || cfg.FeatureFlags.PostHogKey != "" || cfg.FeatureFlags.PostHogHost != "" {
		t.Errorf("expected empty feature flag config, got %+v", cfg.FeatureFlags)
	}
}
