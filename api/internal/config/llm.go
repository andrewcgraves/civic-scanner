package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	LLMGateway   = "gateway"
	LLMAnthropic = "anthropic"
)

var anthropicEfforts = []string{"low", "medium", "high", "xhigh", "max"}

type LLM struct {
	Provider     string
	APIKey       string
	BaseURL      string
	RoutineModel string
	StrongModel  string
	Effort       string
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
	gatewayURL := envOr("AI_GATEWAY_BASE_URL", "https://ai-gateway.vercel.sh/v1")
	if err := checkHTTPURL(gatewayURL); err != nil {
		return LLM{}, fmt.Errorf("AI_GATEWAY_BASE_URL: %w", err)
	}
	anthropicURL := envOr("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
	if err := checkHTTPURL(anthropicURL); err != nil {
		return LLM{}, fmt.Errorf("ANTHROPIC_BASE_URL: %w", err)
	}
	effort := envOr("ANTHROPIC_EFFORT", "low")
	if !slices.Contains(anthropicEfforts, effort) {
		return LLM{}, fmt.Errorf("ANTHROPIC_EFFORT: must be one of %s, got %q", strings.Join(anthropicEfforts, ", "), effort)
	}

	cfg := LLM{Provider: envOr("LLM_PROVIDER", LLMGateway), Timeout: timeout}
	switch cfg.Provider {
	case LLMGateway:
		cfg.APIKey = os.Getenv("AI_GATEWAY_API_KEY")
		cfg.BaseURL = gatewayURL
		cfg.RoutineModel = envOr("LLM_ROUTINE_MODEL", "anthropic/claude-haiku-5.5")
		cfg.StrongModel = envOr("LLM_STRONG_MODEL", "anthropic/claude-sonnet-5.5")
	case LLMAnthropic:
		cfg.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		cfg.BaseURL = anthropicURL
		cfg.RoutineModel = envOr("LLM_ROUTINE_MODEL", "claude-haiku-5-5")
		cfg.StrongModel = envOr("LLM_STRONG_MODEL", "claude-sonnet-5-5")
		cfg.Effort = effort
	default:
		return LLM{}, fmt.Errorf("LLM_PROVIDER: must be %q or %q, got %q", LLMGateway, LLMAnthropic, cfg.Provider)
	}
	for _, m := range []struct{ name, id string }{
		{"LLM_ROUTINE_MODEL", cfg.RoutineModel},
		{"LLM_STRONG_MODEL", cfg.StrongModel},
	} {
		if err := checkModelID(cfg.Provider, m.id); err != nil {
			return LLM{}, fmt.Errorf("%s: %w", m.name, err)
		}
	}
	return cfg, nil
}

func checkModelID(provider, id string) error {
	if strings.ContainsAny(id, " \t\r\n") {
		return fmt.Errorf("model id %q must not contain whitespace", id)
	}
	switch provider {
	case LLMGateway:
		if !strings.Contains(id, "/") {
			return fmt.Errorf("gateway model ids are provider/model (for example anthropic/claude-haiku-5.5), got %q; set LLM_PROVIDER=anthropic to call the Anthropic API directly", id)
		}
	case LLMAnthropic:
		if strings.ContainsAny(id, "/.") {
			return fmt.Errorf("model ids for the Anthropic API have no provider prefix and no dots (for example claude-haiku-5-5), got %q, which looks like a gateway id", id)
		}
	}
	return nil
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
