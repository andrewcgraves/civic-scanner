package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

const (
	TranscriptionGateway = "gateway"
	TranscriptionGroq    = "groq"
)

type Transcription struct {
	Provider          string
	APIKey            string
	BaseURL           string
	Model             string
	Timeout           time.Duration
	DirectUploadBytes int64
	MaxChunkBytes     int64
	ProviderOptions   string
}

func loadTranscription() (Transcription, error) {
	timeout, err := time.ParseDuration(envOr("TRANSCRIPTION_TIMEOUT", "5m"))
	if err != nil {
		return Transcription{}, fmt.Errorf("TRANSCRIPTION_TIMEOUT: %w", err)
	}
	if timeout <= 0 {
		return Transcription{}, fmt.Errorf("TRANSCRIPTION_TIMEOUT: must be positive, got %s", timeout)
	}
	provider := envOr("TRANSCRIPTION_PROVIDER", TranscriptionGateway)
	cfg := Transcription{Provider: provider, Timeout: timeout}
	switch provider {
	case TranscriptionGateway:
		err = loadGatewayTranscription(&cfg)
	case TranscriptionGroq:
		err = loadGroqTranscription(&cfg)
	default:
		err = fmt.Errorf("TRANSCRIPTION_PROVIDER: must be %q or %q, got %q", TranscriptionGateway, TranscriptionGroq, provider)
	}
	if err != nil {
		return Transcription{}, err
	}
	return cfg, nil
}

func loadGatewayTranscription(cfg *Transcription) error {
	baseURL := envOr("AI_GATEWAY_BASE_URL", "https://ai-gateway.vercel.sh/v1")
	if err := checkHTTPURL(baseURL); err != nil {
		return fmt.Errorf("AI_GATEWAY_BASE_URL: %w", err)
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("AI_GATEWAY_BASE_URL: %w", err)
	}
	direct, err := positiveMB("TRANSCRIPTION_DIRECT_UPLOAD_MB", "2")
	if err != nil {
		return err
	}
	chunk, err := positiveMB("TRANSCRIPTION_MAX_CHUNK_MB", "8")
	if err != nil {
		return err
	}
	options := os.Getenv("TRANSCRIPTION_PROVIDER_OPTIONS")
	if options != "" {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(options), &object); err != nil {
			return fmt.Errorf("TRANSCRIPTION_PROVIDER_OPTIONS: must be a JSON object: %w", err)
		}
	}
	cfg.APIKey = os.Getenv("AI_GATEWAY_API_KEY")
	cfg.BaseURL = u.Scheme + "://" + u.Host
	cfg.Model = envOr("TRANSCRIPTION_MODEL", "elevenlabs/scribe-v2")
	cfg.DirectUploadBytes = direct
	cfg.MaxChunkBytes = chunk
	cfg.ProviderOptions = options
	return nil
}

func loadGroqTranscription(cfg *Transcription) error {
	baseURL := envOr("GROQ_BASE_URL", "https://api.groq.com/openai/v1")
	if err := checkHTTPURL(baseURL); err != nil {
		return fmt.Errorf("GROQ_BASE_URL: %w", err)
	}
	upload, err := positiveMB("TRANSCRIPTION_MAX_UPLOAD_MB", "25")
	if err != nil {
		return err
	}
	cfg.APIKey = os.Getenv("GROQ_API_KEY")
	cfg.BaseURL = baseURL
	cfg.Model = envOr("TRANSCRIPTION_MODEL", "whisper-large-v3-turbo")
	cfg.DirectUploadBytes = upload
	cfg.MaxChunkBytes = upload
	return nil
}

func positiveMB(name, fallback string) (int64, error) {
	mb, err := strconv.ParseInt(envOr(name, fallback), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if mb <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %d", name, mb)
	}
	return mb << 20, nil
}
