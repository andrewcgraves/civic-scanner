package config

import (
	"testing"
	"time"
)

var fetchEnv = []string{
	"FETCH_USER_AGENT",
	"FETCH_TIMEOUT",
	"FETCH_MAX_BYTES",
	"FETCH_MAX_REDIRECTS",
	"FIRECRAWL_API_KEY",
	"FIRECRAWL_BASE_URL",
	"FIRECRAWL_TIMEOUT",
}

func clearFetchEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, key := range fetchEnv {
		t.Setenv(key, "")
	}
}

func TestLoadFetchDefaults(t *testing.T) {
	clearFetchEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load without a Firecrawl key: %v", err)
	}
	want := Fetch{
		UserAgent:        "CivicScanner/1.0 (+https://github.com/andrewcgraves/civic-scanner)",
		Timeout:          30 * time.Second,
		MaxBytes:         128 << 20,
		MaxRedirects:     5,
		FirecrawlBaseURL: "https://api.firecrawl.dev",
		FirecrawlTimeout: 90 * time.Second,
	}
	if cfg.Fetch != want {
		t.Errorf("Fetch = %+v, want %+v", cfg.Fetch, want)
	}
}

func TestLoadFetchOverrides(t *testing.T) {
	clearFetchEnv(t)
	t.Setenv("FETCH_USER_AGENT", "Bot/2 (+https://example.test/bot)")
	t.Setenv("FETCH_TIMEOUT", "10s")
	t.Setenv("FETCH_MAX_BYTES", "1000")
	t.Setenv("FETCH_MAX_REDIRECTS", "2")
	t.Setenv("FIRECRAWL_API_KEY", "fc-123")
	t.Setenv("FIRECRAWL_BASE_URL", "http://firecrawl.test")
	t.Setenv("FIRECRAWL_TIMEOUT", "2m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Fetch{
		UserAgent:        "Bot/2 (+https://example.test/bot)",
		Timeout:          10 * time.Second,
		MaxBytes:         1000,
		MaxRedirects:     2,
		FirecrawlAPIKey:  "fc-123",
		FirecrawlBaseURL: "http://firecrawl.test",
		FirecrawlTimeout: 2 * time.Minute,
	}
	if cfg.Fetch != want {
		t.Errorf("Fetch = %+v, want %+v", cfg.Fetch, want)
	}
}

func TestLoadFetchRejectsBadValues(t *testing.T) {
	tests := map[string]string{
		"FETCH_TIMEOUT":       "soon",
		"FIRECRAWL_TIMEOUT":   "0s",
		"FETCH_MAX_BYTES":     "lots",
		"FETCH_MAX_REDIRECTS": "-1",
	}
	for key, value := range tests {
		clearFetchEnv(t)
		t.Setenv(key, value)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%q: Load succeeded, want error", key, value)
		}
	}
}

func TestLoadFetchRejectsNonPositiveBytes(t *testing.T) {
	clearFetchEnv(t)
	t.Setenv("FETCH_MAX_BYTES", "0")
	if _, err := Load(); err == nil {
		t.Error("FETCH_MAX_BYTES=0: Load succeeded, want error")
	}
}
