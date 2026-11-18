package config

import (
	"testing"
	"time"
)

var audioKeys = []string{
	"AUDIO_LIMIT_BINARY", "AUDIO_FFMPEG_BINARY", "AUDIO_FFPROBE_BINARY", "AUDIO_YTDLP_BINARY",
	"AUDIO_TIMEOUT", "AUDIO_MAX_DURATION", "AUDIO_MAX_MEMORY", "AUDIO_MAX_BYTES",
}

func TestLoadAudioDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, key := range audioKeys {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Audio{
		FFmpegBinary:  "ffmpeg",
		FFprobeBinary: "ffprobe",
		YTDLPBinary:   "yt-dlp",
		Timeout:       time.Hour,
		MaxDuration:   12 * time.Hour,
		MaxMemory:     1 << 30,
		MaxBytes:      256 << 20,
	}
	if cfg.Audio != want {
		t.Errorf("Audio = %+v, want %+v", cfg.Audio, want)
	}
}

func TestLoadAudioOverride(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	values := map[string]string{
		"AUDIO_LIMIT_BINARY":   "/opt/civic/limit",
		"AUDIO_FFMPEG_BINARY":  "/usr/bin/ffmpeg",
		"AUDIO_FFPROBE_BINARY": "/usr/bin/ffprobe",
		"AUDIO_YTDLP_BINARY":   "/usr/local/bin/yt-dlp",
		"AUDIO_TIMEOUT":        "30m",
		"AUDIO_MAX_DURATION":   "6h",
		"AUDIO_MAX_MEMORY":     "536870912",
		"AUDIO_MAX_BYTES":      "1048576",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Audio{
		LimitBinary:   "/opt/civic/limit",
		FFmpegBinary:  "/usr/bin/ffmpeg",
		FFprobeBinary: "/usr/bin/ffprobe",
		YTDLPBinary:   "/usr/local/bin/yt-dlp",
		Timeout:       30 * time.Minute,
		MaxDuration:   6 * time.Hour,
		MaxMemory:     512 << 20,
		MaxBytes:      1 << 20,
	}
	if cfg.Audio != want {
		t.Errorf("Audio = %+v, want %+v", cfg.Audio, want)
	}
}

func TestLoadAudioErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	cases := map[string]string{
		"AUDIO_TIMEOUT":      "0s",
		"AUDIO_MAX_DURATION": "forever",
		"AUDIO_MAX_MEMORY":   "-1",
		"AUDIO_MAX_BYTES":    "lots",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Errorf("%s=%s: expected an error", key, value)
			}
		})
	}
}
