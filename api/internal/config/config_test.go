package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("PORT", "")
	t.Setenv("APP_ENV", "")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "8080" || cfg.AppEnv != "development" || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Error("expected error when DATABASE_URL is unset")
	}

	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("LOG_LEVEL", "loud")
	if _, err := Load(); err == nil {
		t.Error("expected error for invalid LOG_LEVEL")
	}
}

func TestNewLogger(t *testing.T) {
	logger := NewLogger(Config{LogLevel: slog.LevelWarn, AppEnv: "test"})
	if logger.Enabled(t.Context(), slog.LevelInfo) {
		t.Error("info enabled at warn level")
	}
	if !logger.Enabled(t.Context(), slog.LevelWarn) {
		t.Error("warn disabled at warn level")
	}
}
