package config

import (
	"slices"
	"testing"
	"time"
)

func TestLoadFeatureFlags(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("FEATURE_FLAGS", " new-search, ,digest-emails ,")
	t.Setenv("POSTHOG_API_KEY", "phc_test")
	t.Setenv("POSTHOG_HOST", "https://eu.i.posthog.com")
	t.Setenv("POSTHOG_FLAG_TIMEOUT", "250ms")
	t.Setenv("POSTHOG_FLAG_CACHE_TTL", "45s")

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
	if cfg.FeatureFlags.PostHogCacheTTL != 45*time.Second {
		t.Errorf("PostHogCacheTTL = %v, want 45s", cfg.FeatureFlags.PostHogCacheTTL)
	}
	if cfg.FeatureFlags.PostHogTimeout != 250*time.Millisecond {
		t.Errorf("PostHogTimeout = %v, want 250ms", cfg.FeatureFlags.PostHogTimeout)
	}
}

func TestLoadFeatureFlagsUnset(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("FEATURE_FLAGS", "")
	t.Setenv("POSTHOG_API_KEY", "")
	t.Setenv("POSTHOG_HOST", "")
	t.Setenv("POSTHOG_FLAG_TIMEOUT", "")
	t.Setenv("POSTHOG_FLAG_CACHE_TTL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.FeatureFlags.On) != 0 || cfg.FeatureFlags.PostHogKey != "" || cfg.FeatureFlags.PostHogHost != "" {
		t.Errorf("expected empty feature flag config, got %+v", cfg.FeatureFlags)
	}
	if cfg.FeatureFlags.PostHogCacheTTL != 30*time.Second {
		t.Errorf("PostHogCacheTTL = %v, want the 30s default", cfg.FeatureFlags.PostHogCacheTTL)
	}
	if cfg.FeatureFlags.PostHogTimeout != time.Second {
		t.Errorf("PostHogTimeout = %v, want the 1s default", cfg.FeatureFlags.PostHogTimeout)
	}
}

func TestLoadRejectsInvalidPostHogFlagTimeout(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, value := range []string{"soon", "0s", "-1s"} {
		t.Setenv("POSTHOG_FLAG_TIMEOUT", value)
		if _, err := Load(); err == nil {
			t.Errorf("POSTHOG_FLAG_TIMEOUT=%q: expected an error", value)
		}
	}
}

func TestLoadAllowsZeroPostHogFlagCacheTTL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("POSTHOG_FLAG_CACHE_TTL", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FeatureFlags.PostHogCacheTTL != 0 {
		t.Errorf("PostHogCacheTTL = %v, want 0 to disable the cache", cfg.FeatureFlags.PostHogCacheTTL)
	}
}

func TestLoadRejectsInvalidPostHogFlagCacheTTL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, value := range []string{"soon", "-1s"} {
		t.Setenv("POSTHOG_FLAG_CACHE_TTL", value)
		if _, err := Load(); err == nil {
			t.Errorf("POSTHOG_FLAG_CACHE_TTL=%q: expected an error", value)
		}
	}
}
