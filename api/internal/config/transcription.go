package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Transcription struct {
	APIKey         string
	BaseURL        string
	Model          string
	Timeout        time.Duration
	MaxUploadBytes int64
}

func loadTranscription() (Transcription, error) {
	timeout, err := time.ParseDuration(envOr("TRANSCRIPTION_TIMEOUT", "5m"))
	if err != nil {
		return Transcription{}, fmt.Errorf("TRANSCRIPTION_TIMEOUT: %w", err)
	}
	if timeout <= 0 {
		return Transcription{}, fmt.Errorf("TRANSCRIPTION_TIMEOUT: must be positive, got %s", timeout)
	}
	baseURL := envOr("GROQ_BASE_URL", "https://api.groq.com/openai/v1")
	if err := checkHTTPURL(baseURL); err != nil {
		return Transcription{}, fmt.Errorf("GROQ_BASE_URL: %w", err)
	}
	uploadMB, err := strconv.ParseInt(envOr("TRANSCRIPTION_MAX_UPLOAD_MB", "25"), 10, 64)
	if err != nil {
		return Transcription{}, fmt.Errorf("TRANSCRIPTION_MAX_UPLOAD_MB: %w", err)
	}
	if uploadMB <= 0 {
		return Transcription{}, fmt.Errorf("TRANSCRIPTION_MAX_UPLOAD_MB: must be positive, got %d", uploadMB)
	}
	return Transcription{
		APIKey:         os.Getenv("GROQ_API_KEY"),
		BaseURL:        baseURL,
		Model:          envOr("TRANSCRIPTION_MODEL", "whisper-large-v3-turbo"),
		Timeout:        timeout,
		MaxUploadBytes: uploadMB << 20,
	}, nil
}
