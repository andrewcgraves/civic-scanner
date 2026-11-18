package config

import (
	"testing"
	"time"
)

func TestLoadLLMDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("AI_GATEWAY_API_KEY", "")
	t.Setenv("AI_GATEWAY_BASE_URL", "")
	t.Setenv("LLM_ROUTINE_MODEL", "")
	t.Setenv("LLM_STRONG_MODEL", "")
	t.Setenv("LLM_TIMEOUT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load without a gateway key: %v", err)
	}
	want := LLM{
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
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
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

func TestLoadLLMBaseURLErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("LLM_TIMEOUT", "")
	for _, v := range []string{"ai-gateway.vercel.sh/v1", "ftp://ai-gateway.vercel.sh/v1", "https:///v1", "http://bad host/v1", "://"} {
		t.Setenv("AI_GATEWAY_BASE_URL", v)
		if _, err := Load(); err == nil {
			t.Errorf("expected error for AI_GATEWAY_BASE_URL=%q", v)
		}
	}
}

func TestLoadLLMTimeoutErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, v := range []string{"soon", "0s", "-5s"} {
		t.Setenv("LLM_TIMEOUT", v)
		if _, err := Load(); err == nil {
			t.Errorf("expected error for LLM_TIMEOUT=%q", v)
		}
	}
}
