package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadIngestDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("INGEST_CHECK_HOUR_UTC", "")
	t.Setenv("INGEST_AGENDA_REFRESH_GRACE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ingest != (Ingest{CheckHourUTC: 13, AgendaRefreshGrace: 48 * time.Hour}) {
		t.Errorf("Ingest = %+v, want CheckHourUTC 13 and AgendaRefreshGrace 48h", cfg.Ingest)
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

func TestLoadIngestAgendaRefreshGraceOverride(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for value, want := range map[string]time.Duration{
		"48h":    48 * time.Hour,
		"72h":    72 * time.Hour,
		"36h30m": 36*time.Hour + 30*time.Minute,
		"1m":     time.Minute,
		"2160h":  2160 * time.Hour,
	} {
		t.Setenv("INGEST_AGENDA_REFRESH_GRACE", value)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(%s): %v", value, err)
		}
		if cfg.Ingest.AgendaRefreshGrace != want {
			t.Errorf("INGEST_AGENDA_REFRESH_GRACE=%s: AgendaRefreshGrace = %s, want %s", value, cfg.Ingest.AgendaRefreshGrace, want)
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

func TestLoadIngestAgendaRefreshGraceErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, value := range []string{"0", "0s", "-48h", "48", "2 days", "2d", "soon"} {
		t.Setenv("INGEST_AGENDA_REFRESH_GRACE", value)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "INGEST_AGENDA_REFRESH_GRACE") {
			t.Errorf("INGEST_AGENDA_REFRESH_GRACE=%s: error = %v, want one naming the variable", value, err)
		}
	}
}
