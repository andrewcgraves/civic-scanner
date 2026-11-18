package config

import (
	"testing"
	"time"
)

func TestLoadTranscriptionDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("GROQ_BASE_URL", "")
	t.Setenv("TRANSCRIPTION_MODEL", "")
	t.Setenv("TRANSCRIPTION_TIMEOUT", "")
	t.Setenv("TRANSCRIPTION_MAX_UPLOAD_MB", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load without a Groq key: %v", err)
	}
	want := Transcription{
		BaseURL:        "https://api.groq.com/openai/v1",
		Model:          "whisper-large-v3-turbo",
		Timeout:        5 * time.Minute,
		MaxUploadBytes: 25 << 20,
	}
	if cfg.Transcription != want {
		t.Errorf("Transcription = %+v, want %+v", cfg.Transcription, want)
	}
}

func TestLoadTranscriptionOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("GROQ_API_KEY", "gsk-123")
	t.Setenv("GROQ_BASE_URL", "http://groq.test/openai/v1")
	t.Setenv("TRANSCRIPTION_MODEL", "whisper-large-v3")
	t.Setenv("TRANSCRIPTION_TIMEOUT", "90s")
	t.Setenv("TRANSCRIPTION_MAX_UPLOAD_MB", "100")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Transcription{
		APIKey:         "gsk-123",
		BaseURL:        "http://groq.test/openai/v1",
		Model:          "whisper-large-v3",
		Timeout:        90 * time.Second,
		MaxUploadBytes: 100 << 20,
	}
	if cfg.Transcription != want {
		t.Errorf("Transcription = %+v, want %+v", cfg.Transcription, want)
	}
}

func TestLoadTranscriptionErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"bad base url":   {"GROQ_BASE_URL": "api.groq.com"},
		"bad timeout":    {"TRANSCRIPTION_TIMEOUT": "soon"},
		"zero timeout":   {"TRANSCRIPTION_TIMEOUT": "0s"},
		"bad upload":     {"TRANSCRIPTION_MAX_UPLOAD_MB": "lots"},
		"zero upload":    {"TRANSCRIPTION_MAX_UPLOAD_MB": "0"},
		"negative limit": {"TRANSCRIPTION_MAX_UPLOAD_MB": "-4"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/test")
			t.Setenv("GROQ_BASE_URL", "")
			t.Setenv("TRANSCRIPTION_TIMEOUT", "")
			t.Setenv("TRANSCRIPTION_MAX_UPLOAD_MB", "")
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Errorf("expected an error for %v", env)
			}
		})
	}
}
