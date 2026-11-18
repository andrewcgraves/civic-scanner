package config

import (
	"testing"
	"time"
)

func TestLoadExtractDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, key := range []string{"EXTRACT_PDF_BINARY", "EXTRACT_PDF_TIMEOUT", "EXTRACT_PDF_MAX_MEMORY", "EXTRACT_MAX_TEXT_BYTES"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Extract{PDFTimeout: time.Minute, PDFMaxMemory: 512 << 20, MaxTextBytes: 8 << 20}
	if cfg.Extract != want {
		t.Errorf("Extract = %+v, want %+v", cfg.Extract, want)
	}
}

func TestLoadExtractOverride(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("EXTRACT_PDF_BINARY", "/opt/civic/extract")
	t.Setenv("EXTRACT_PDF_TIMEOUT", "5s")
	t.Setenv("EXTRACT_PDF_MAX_MEMORY", "1048576")
	t.Setenv("EXTRACT_MAX_TEXT_BYTES", "4096")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Extract{PDFBinary: "/opt/civic/extract", PDFTimeout: 5 * time.Second, PDFMaxMemory: 1 << 20, MaxTextBytes: 4096}
	if cfg.Extract != want {
		t.Errorf("Extract = %+v, want %+v", cfg.Extract, want)
	}
}

func TestLoadExtractErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	cases := map[string]string{
		"EXTRACT_PDF_TIMEOUT":    "0s",
		"EXTRACT_PDF_MAX_MEMORY": "-1",
		"EXTRACT_MAX_TEXT_BYTES": "lots",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Errorf("%s=%s: expected an error", key, value)
			}
		})
	}
}
