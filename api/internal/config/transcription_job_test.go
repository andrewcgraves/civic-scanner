package config

import (
	"testing"
	"time"
)

var transcriptionJobKeys = []string{
	"TRANSCRIPTION_JOB_TIMEOUT", "TRANSCRIPTION_MAX_DURATION", "TRANSCRIPTION_MAX_AUDIO_BYTES",
	"TRANSCRIPTION_CONCURRENCY", "TRANSCRIPTION_CHUNK_RETRIES",
}

func clearTranscriptionJobEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, key := range transcriptionJobKeys {
		t.Setenv(key, "")
	}
}

func TestLoadTranscriptionJobDefaults(t *testing.T) {
	clearTranscriptionJobEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := TranscriptionJob{
		Timeout:       2 * time.Hour,
		MaxDuration:   8 * time.Hour,
		MaxAudioBytes: 128 << 20,
		Concurrency:   3,
		ChunkRetries:  3,
	}
	if cfg.Transcribe != want {
		t.Errorf("Transcribe = %+v, want %+v", cfg.Transcribe, want)
	}
}

func TestLoadTranscriptionJobOverrides(t *testing.T) {
	clearTranscriptionJobEnv(t)
	t.Setenv("TRANSCRIPTION_JOB_TIMEOUT", "90m")
	t.Setenv("TRANSCRIPTION_MAX_DURATION", "6h")
	t.Setenv("TRANSCRIPTION_MAX_AUDIO_BYTES", "1048576")
	t.Setenv("TRANSCRIPTION_CONCURRENCY", "1")
	t.Setenv("TRANSCRIPTION_CHUNK_RETRIES", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := TranscriptionJob{Timeout: 90 * time.Minute, MaxDuration: 6 * time.Hour, MaxAudioBytes: 1 << 20, Concurrency: 1}
	if cfg.Transcribe != want {
		t.Errorf("Transcribe = %+v, want %+v", cfg.Transcribe, want)
	}
}

func TestLoadTranscriptionJobRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"TRANSCRIPTION_JOB_TIMEOUT":     "0s",
		"TRANSCRIPTION_MAX_DURATION":    "soon",
		"TRANSCRIPTION_MAX_AUDIO_BYTES": "-1",
		"TRANSCRIPTION_CONCURRENCY":     "9",
		"TRANSCRIPTION_CHUNK_RETRIES":   "many",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			clearTranscriptionJobEnv(t)
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Errorf("Load with %s=%s succeeded", key, value)
			}
		})
	}
}
