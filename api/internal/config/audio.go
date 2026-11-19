package config

import (
	"os"
	"time"
)

type Audio struct {
	LimitBinary   string
	FFmpegBinary  string
	FFprobeBinary string
	YTDLPBinary   string
	Timeout       time.Duration
	MaxDuration   time.Duration
	MaxMemory     int64
	MaxBytes      int64
}

func loadAudio() (Audio, error) {
	timeout, err := positiveDuration("AUDIO_TIMEOUT", "60m")
	if err != nil {
		return Audio{}, err
	}
	maxDuration, err := positiveDuration("AUDIO_MAX_DURATION", "12h")
	if err != nil {
		return Audio{}, err
	}
	maxMemory, err := positiveInt("AUDIO_MAX_MEMORY", "1073741824")
	if err != nil {
		return Audio{}, err
	}
	maxBytes, err := positiveInt("AUDIO_MAX_BYTES", "268435456")
	if err != nil {
		return Audio{}, err
	}
	return Audio{
		LimitBinary:   os.Getenv("AUDIO_LIMIT_BINARY"),
		FFmpegBinary:  envOr("AUDIO_FFMPEG_BINARY", "ffmpeg"),
		FFprobeBinary: envOr("AUDIO_FFPROBE_BINARY", "ffprobe"),
		YTDLPBinary:   envOr("AUDIO_YTDLP_BINARY", "yt-dlp"),
		Timeout:       timeout,
		MaxDuration:   maxDuration,
		MaxMemory:     maxMemory,
		MaxBytes:      maxBytes,
	}, nil
}
