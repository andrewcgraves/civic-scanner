package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
)

type Config struct {
	Port        string
	DatabaseURL string
	LogLevel    slog.Level
	AppEnv      string
	LLM         LLM
	Fetch       Fetch

	FeatureFlags FeatureFlags
}

func Load() (Config, error) {
	cfg := Config{
		Port:        envOr("PORT", "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		AppEnv:      envOr("APP_ENV", "development"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(envOr("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	llm, err := loadLLM()
	if err != nil {
		return Config{}, err
	}
	cfg.LLM = llm
	if cfg.Fetch, err = loadFetch(); err != nil {
		return Config{}, err
	}
	if cfg.FeatureFlags, err = loadFeatureFlags(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func NewLogger(cfg Config) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	return slog.New(h).With("env", cfg.AppEnv)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
