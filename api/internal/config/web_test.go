package config

import "testing"

func TestLoadWebUnsetIsEmpty(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("PUBLIC_WEB_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Web != (Web{}) {
		t.Errorf("Web = %+v, want empty", cfg.Web)
	}
}

func TestLoadWebPublicURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for value, want := range map[string]string{
		"https://civicscanner.org":       "https://civicscanner.org",
		" https://civicscanner.org/ ":    "https://civicscanner.org",
		"http://localhost:3000":          "http://localhost:3000",
		"https://example.com/civic/":     "https://example.com/civic",
		"https://preview.example.com///": "https://preview.example.com",
	} {
		t.Setenv("PUBLIC_WEB_URL", value)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("PUBLIC_WEB_URL=%q: %v", value, err)
		}
		if cfg.Web.PublicURL != want {
			t.Errorf("PUBLIC_WEB_URL=%q: PublicURL = %q, want %q", value, cfg.Web.PublicURL, want)
		}
	}
}

func TestLoadWebErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, value := range []string{
		"civicscanner.org",
		"javascript:alert(1)",
		"ftp://example.com",
		"https://",
		"https://user:pass@example.com",
		"https://example.com/?a=1",
		"https://example.com/?",
		"https://example.com/#top",
		"https://exa mple.com",
	} {
		t.Setenv("PUBLIC_WEB_URL", value)
		if _, err := Load(); err == nil {
			t.Errorf("PUBLIC_WEB_URL=%q: expected an error", value)
		}
	}
}
