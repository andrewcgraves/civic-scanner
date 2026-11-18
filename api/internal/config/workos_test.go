package config

import (
	"slices"
	"strings"
	"testing"
)

func setWorkOSEnv(t *testing.T, clientID, baseURL, issuer, audience string) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("WORKOS_CLIENT_ID", clientID)
	t.Setenv("WORKOS_BASE_URL", baseURL)
	t.Setenv("WORKOS_ISSUER", issuer)
	t.Setenv("WORKOS_AUDIENCE", audience)
}

func TestLoadWorkOSUnconfigured(t *testing.T) {
	setWorkOSEnv(t, "", "", "", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load without a client id: %v", err)
	}
	if cfg.WorkOS.Configured() {
		t.Errorf("WorkOS = %+v, want not configured", cfg.WorkOS)
	}
	if cfg.WorkOS.BaseURL != "https://api.workos.com" || cfg.WorkOS.Issuers != nil {
		t.Errorf("WorkOS = %+v", cfg.WorkOS)
	}
}

func TestLoadWorkOSDefaults(t *testing.T) {
	setWorkOSEnv(t, " client_123 ", "", "", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w := cfg.WorkOS
	if !w.Configured() || w.ClientID != "client_123" || w.Audience != "" {
		t.Errorf("WorkOS = %+v", w)
	}
	if want := []string{"https://api.workos.com/user_management/client_123"}; !slices.Equal(w.Issuers, want) {
		t.Errorf("Issuers = %v, want %v", w.Issuers, want)
	}
	if got, want := w.JWKSURL(), "https://api.workos.com/sso/jwks/client_123"; got != want {
		t.Errorf("JWKSURL = %q, want %q", got, want)
	}
}

func TestLoadWorkOSOverrides(t *testing.T) {
	setWorkOSEnv(t, "client_123", "https://auth.example.com/", "https://api.workos.com, https://auth.example.com", "https://api.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w := cfg.WorkOS
	if want := []string{"https://api.workos.com", "https://auth.example.com"}; !slices.Equal(w.Issuers, want) {
		t.Errorf("Issuers = %v, want %v", w.Issuers, want)
	}
	if w.Audience != "https://api.example.com" {
		t.Errorf("Audience = %q", w.Audience)
	}
	if got, want := w.JWKSURL(), "https://auth.example.com/sso/jwks/client_123"; got != want {
		t.Errorf("JWKSURL = %q, want %q", got, want)
	}
}

func TestLoadWorkOSRejectsBadBaseURL(t *testing.T) {
	for _, raw := range []string{
		"http://api.workos.com",
		"api.workos.com",
		"https://",
		"https://api.workos.com/sso",
		"https://api.workos.com?x=1",
		"https://user@api.workos.com",
		"https://api.workos.com/#frag",
		"://bad",
	} {
		t.Run(raw, func(t *testing.T) {
			setWorkOSEnv(t, "", raw, "", "")
			_, err := Load()
			if err == nil || !strings.HasPrefix(err.Error(), "WORKOS_BASE_URL:") {
				t.Errorf("Load = %v, want a WORKOS_BASE_URL error", err)
			}
		})
	}
}
