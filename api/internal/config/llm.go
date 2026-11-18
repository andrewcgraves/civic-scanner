package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"
)

type LLM struct {
	APIKey       string
	BaseURL      string
	RoutineModel string
	StrongModel  string
	Timeout      time.Duration
}

func loadLLM() (LLM, error) {
	timeout, err := time.ParseDuration(envOr("LLM_TIMEOUT", "2m"))
	if err != nil {
		return LLM{}, fmt.Errorf("LLM_TIMEOUT: %w", err)
	}
	if timeout <= 0 {
		return LLM{}, fmt.Errorf("LLM_TIMEOUT: must be positive, got %s", timeout)
	}
	baseURL := envOr("AI_GATEWAY_BASE_URL", "https://ai-gateway.vercel.sh/v1")
	if err := checkHTTPURL(baseURL); err != nil {
		return LLM{}, fmt.Errorf("AI_GATEWAY_BASE_URL: %w", err)
	}
	return LLM{
		APIKey:       os.Getenv("AI_GATEWAY_API_KEY"),
		BaseURL:      baseURL,
		RoutineModel: envOr("LLM_ROUTINE_MODEL", "anthropic/claude-haiku-5.5"),
		StrongModel:  envOr("LLM_STRONG_MODEL", "anthropic/claude-sonnet-5.5"),
		Timeout:      timeout,
	}, nil
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", raw)
	}
	if u.Host == "" {
		return errors.New("host is required")
	}
	return nil
}
