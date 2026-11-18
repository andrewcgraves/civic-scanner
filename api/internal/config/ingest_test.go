package config

import "testing"

func TestLoadIngestDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("INGEST_CHECK_HOUR_UTC", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ingest != (Ingest{CheckHourUTC: 13}) {
		t.Errorf("Ingest = %+v, want CheckHourUTC 13", cfg.Ingest)
	}
}

func TestLoadIngestOverride(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for value, want := range map[string]int{"0": 0, "23": 23, "7": 7} {
		t.Setenv("INGEST_CHECK_HOUR_UTC", value)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(%s): %v", value, err)
		}
		if cfg.Ingest.CheckHourUTC != want {
			t.Errorf("INGEST_CHECK_HOUR_UTC=%s: CheckHourUTC = %d, want %d", value, cfg.Ingest.CheckHourUTC, want)
		}
	}
}

func TestLoadIngestErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, value := range []string{"noon", "-1", "24", "13.5"} {
		t.Setenv("INGEST_CHECK_HOUR_UTC", value)
		if _, err := Load(); err == nil {
			t.Errorf("INGEST_CHECK_HOUR_UTC=%s: expected an error", value)
		}
	}
}
