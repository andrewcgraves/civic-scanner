package config

import (
	"strings"
	"testing"
	"time"
)

var llmVariables = []string{
	"LLM_PROVIDER", "AI_GATEWAY_API_KEY", "AI_GATEWAY_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL",
	"ANTHROPIC_EFFORT", "LLM_ROUTINE_MODEL", "LLM_STRONG_MODEL", "LLM_TIMEOUT",
}

func clearLLMEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, name := range llmVariables {
		t.Setenv(name, "")
	}
}

func TestLoadLLMDefaults(t *testing.T) {
	clearLLMEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load without a gateway key: %v", err)
	}
	want := LLM{
		Provider:     LLMGateway,
		BaseURL:      "https://ai-gateway.vercel.sh/v1",
		RoutineModel: "anthropic/claude-haiku-5.5",
		StrongModel:  "anthropic/claude-sonnet-5.5",
		Timeout:      2 * time.Minute,
	}
	if cfg.LLM != want {
		t.Errorf("LLM = %+v, want %+v", cfg.LLM, want)
	}
}

func TestLoadLLMExistingGatewayDeploymentIsUnchanged(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("AI_GATEWAY_API_KEY", "vck-123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := LLM{
		Provider:     LLMGateway,
		APIKey:       "vck-123",
		BaseURL:      "https://ai-gateway.vercel.sh/v1",
		RoutineModel: "anthropic/claude-haiku-5.5",
		StrongModel:  "anthropic/claude-sonnet-5.5",
		Timeout:      2 * time.Minute,
	}
	if cfg.LLM != want {
		t.Errorf("LLM = %+v, want %+v", cfg.LLM, want)
	}
}

func TestLoadLLMOverrides(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("AI_GATEWAY_API_KEY", "key-123")
	t.Setenv("AI_GATEWAY_BASE_URL", "http://gateway.test/v1")
	t.Setenv("LLM_ROUTINE_MODEL", "openai/gpt-cheap")
	t.Setenv("LLM_STRONG_MODEL", "openai/gpt-big")
	t.Setenv("LLM_TIMEOUT", "45s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := LLM{
		Provider:     LLMGateway,
		APIKey:       "key-123",
		BaseURL:      "http://gateway.test/v1",
		RoutineModel: "openai/gpt-cheap",
		StrongModel:  "openai/gpt-big",
		Timeout:      45 * time.Second,
	}
	if cfg.LLM != want {
		t.Errorf("LLM = %+v, want %+v", cfg.LLM, want)
	}
}

func TestLoadLLMGatewayIgnoresTheAnthropicKey(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key-123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.Provider != LLMGateway || cfg.LLM.APIKey != "" {
		t.Errorf("LLM = %+v, want the gateway without a key", cfg.LLM)
	}
}

func TestLoadLLMAnthropicDefaults(t *testing.T) {
	clearLLMEnv(t)
	key := "anthropic-key-123"
	t.Setenv("LLM_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", key)
	t.Setenv("AI_GATEWAY_API_KEY", "vck-ignored")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := LLM{
		Provider:     LLMAnthropic,
		APIKey:       key,
		BaseURL:      "https://api.anthropic.com",
		RoutineModel: "claude-haiku-5-5",
		StrongModel:  "claude-sonnet-5-5",
		Effort:       "low",
		Timeout:      2 * time.Minute,
	}
	if cfg.LLM != want {
		t.Errorf("LLM = %+v, want %+v", cfg.LLM, want)
	}
}

func TestLoadLLMAnthropicWithoutKeyStartsUnconfigured(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_PROVIDER", "anthropic")
	t.Setenv("AI_GATEWAY_API_KEY", "vck-ignored")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.Provider != LLMAnthropic || cfg.LLM.APIKey != "" {
		t.Errorf("LLM = %+v, want the anthropic provider without a key", cfg.LLM)
	}
}

func TestLoadLLMAnthropicOverrides(t *testing.T) {
	clearLLMEnv(t)
	key := "anthropic-key-123"
	t.Setenv("LLM_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", key)
	t.Setenv("ANTHROPIC_BASE_URL", "http://anthropic.test:8080")
	t.Setenv("ANTHROPIC_EFFORT", "medium")
	t.Setenv("LLM_ROUTINE_MODEL", "claude-haiku-4-5")
	t.Setenv("LLM_STRONG_MODEL", "claude-opus-5-5")
	t.Setenv("LLM_TIMEOUT", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := LLM{
		Provider:     LLMAnthropic,
		APIKey:       key,
		BaseURL:      "http://anthropic.test:8080",
		RoutineModel: "claude-haiku-4-5",
		StrongModel:  "claude-opus-5-5",
		Effort:       "medium",
		Timeout:      30 * time.Second,
	}
	if cfg.LLM != want {
		t.Errorf("LLM = %+v, want %+v", cfg.LLM, want)
	}
}

func TestLoadLLMAcceptsEveryEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		clearLLMEnv(t)
		t.Setenv("LLM_PROVIDER", "anthropic")
		t.Setenv("ANTHROPIC_EFFORT", effort)
		cfg, err := Load()
		if err != nil || cfg.LLM.Effort != effort {
			t.Errorf("ANTHROPIC_EFFORT=%s: effort %q, err %v", effort, cfg.LLM.Effort, err)
		}
	}
}

func TestLoadLLMRejectsInvalidValues(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"unknown provider":           {map[string]string{"LLM_PROVIDER": "openai"}, "LLM_PROVIDER"},
		"provider in capitals":       {map[string]string{"LLM_PROVIDER": "Anthropic"}, "LLM_PROVIDER"},
		"unknown effort":             {map[string]string{"LLM_PROVIDER": "anthropic", "ANTHROPIC_EFFORT": "none"}, "ANTHROPIC_EFFORT"},
		"bad effort with gateway":    {map[string]string{"ANTHROPIC_EFFORT": "minimal"}, "ANTHROPIC_EFFORT"},
		"bad anthropic url":          {map[string]string{"LLM_PROVIDER": "anthropic", "ANTHROPIC_BASE_URL": "api.anthropic.com"}, "ANTHROPIC_BASE_URL"},
		"bad anthropic url, gateway": {map[string]string{"ANTHROPIC_BASE_URL": "ftp://api.anthropic.com"}, "ANTHROPIC_BASE_URL"},
		"bad gateway url, anthropic": {map[string]string{"LLM_PROVIDER": "anthropic", "AI_GATEWAY_BASE_URL": "gateway"}, "AI_GATEWAY_BASE_URL"},
		"gateway routine id with anthropic": {
			map[string]string{"LLM_PROVIDER": "anthropic", "LLM_ROUTINE_MODEL": "anthropic/claude-haiku-5.5"}, "LLM_ROUTINE_MODEL",
		},
		"dotted strong id with anthropic": {
			map[string]string{"LLM_PROVIDER": "anthropic", "LLM_STRONG_MODEL": "claude-sonnet-5.5"}, "LLM_STRONG_MODEL",
		},
		"anthropic routine id with gateway": {map[string]string{"LLM_ROUTINE_MODEL": "claude-haiku-5-5"}, "LLM_ROUTINE_MODEL"},
		"anthropic strong id with gateway":  {map[string]string{"LLM_STRONG_MODEL": "claude-sonnet-5-5"}, "LLM_STRONG_MODEL"},
		"model id with a space":             {map[string]string{"LLM_PROVIDER": "anthropic", "LLM_ROUTINE_MODEL": "claude haiku"}, "LLM_ROUTINE_MODEL"},
		"model id with a trailing newline":  {map[string]string{"LLM_ROUTINE_MODEL": "anthropic/claude-haiku-5.5\n"}, "LLM_ROUTINE_MODEL"},
	} {
		t.Run(name, func(t *testing.T) {
			clearLLMEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil || !strings.HasPrefix(err.Error(), tc.want+":") {
				t.Errorf("err = %v, want an error naming %s", err, tc.want)
			}
		})
	}
}

func TestLoadLLMBaseURLErrors(t *testing.T) {
	clearLLMEnv(t)
	for _, v := range []string{"ai-gateway.vercel.sh/v1", "ftp://ai-gateway.vercel.sh/v1", "https:///v1", "http://bad host/v1", "://"} {
		t.Setenv("AI_GATEWAY_BASE_URL", v)
		if _, err := Load(); err == nil {
			t.Errorf("expected error for AI_GATEWAY_BASE_URL=%q", v)
		}
	}
}

func TestLoadLLMTimeoutErrors(t *testing.T) {
	clearLLMEnv(t)
	for _, v := range []string{"soon", "0s", "-5s"} {
		t.Setenv("LLM_TIMEOUT", v)
		if _, err := Load(); err == nil {
			t.Errorf("expected error for LLM_TIMEOUT=%q", v)
		}
	}
}
