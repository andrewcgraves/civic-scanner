package config

import (
	"testing"
	"time"
)

func clearTranscriptionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, k := range []string{
		"TRANSCRIPTION_PROVIDER", "TRANSCRIPTION_MODEL", "TRANSCRIPTION_TIMEOUT",
		"TRANSCRIPTION_DIRECT_UPLOAD_MB", "TRANSCRIPTION_MAX_CHUNK_MB", "TRANSCRIPTION_PROVIDER_OPTIONS",
		"AI_GATEWAY_API_KEY", "AI_GATEWAY_BASE_URL",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadTranscriptionGatewayDefaults(t *testing.T) {
	clearTranscriptionEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load without a gateway key: %v", err)
	}
	want := Transcription{
		Provider:          TranscriptionGateway,
		BaseURL:           "https://ai-gateway.vercel.sh",
		Model:             "elevenlabs/scribe-v2",
		Timeout:           5 * time.Minute,
		DirectUploadBytes: 2 << 20,
		MaxChunkBytes:     8 << 20,
	}
	if cfg.Transcription != want {
		t.Errorf("Transcription = %+v, want %+v", cfg.Transcription, want)
	}
}

func TestLoadTranscriptionGatewayOverrides(t *testing.T) {
	clearTranscriptionEnv(t)
	t.Setenv("AI_GATEWAY_API_KEY", "vck-123")
	t.Setenv("AI_GATEWAY_BASE_URL", "http://gateway.test:8080/v1/")
	t.Setenv("TRANSCRIPTION_MODEL", "microsoft/mai-transcribe-2")
	t.Setenv("TRANSCRIPTION_TIMEOUT", "90s")
	t.Setenv("TRANSCRIPTION_DIRECT_UPLOAD_MB", "1")
	t.Setenv("TRANSCRIPTION_MAX_CHUNK_MB", "4")
	t.Setenv("TRANSCRIPTION_PROVIDER_OPTIONS", `{"azure":{"diarization":{"enabled":true}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Transcription{
		Provider:          TranscriptionGateway,
		APIKey:            "vck-123",
		BaseURL:           "http://gateway.test:8080",
		Model:             "microsoft/mai-transcribe-2",
		Timeout:           90 * time.Second,
		DirectUploadBytes: 1 << 20,
		MaxChunkBytes:     4 << 20,
		ProviderOptions:   `{"azure":{"diarization":{"enabled":true}}}`,
	}
	if cfg.Transcription != want {
		t.Errorf("Transcription = %+v, want %+v", cfg.Transcription, want)
	}
}

func TestLoadTranscriptionErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown provider":      {"TRANSCRIPTION_PROVIDER": "whisper"},
		"bad gateway url":       {"AI_GATEWAY_BASE_URL": "gateway"},
		"groq provider":         {"TRANSCRIPTION_PROVIDER": "groq"},
		"bad timeout":           {"TRANSCRIPTION_TIMEOUT": "soon"},
		"zero timeout":          {"TRANSCRIPTION_TIMEOUT": "0s"},
		"bad direct upload":     {"TRANSCRIPTION_DIRECT_UPLOAD_MB": "lots"},
		"zero direct upload":    {"TRANSCRIPTION_DIRECT_UPLOAD_MB": "0"},
		"bad chunk limit":       {"TRANSCRIPTION_MAX_CHUNK_MB": "lots"},
		"zero chunk limit":      {"TRANSCRIPTION_MAX_CHUNK_MB": "0"},
		"provider options text": {"TRANSCRIPTION_PROVIDER_OPTIONS": "diarize"},
		"provider options list": {"TRANSCRIPTION_PROVIDER_OPTIONS": "[1]"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			clearTranscriptionEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Errorf("expected an error for %v", env)
			}
		})
	}
}
