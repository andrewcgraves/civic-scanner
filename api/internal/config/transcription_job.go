package config

import (
	"fmt"
	"strconv"
	"time"
)

const (
	maxTranscriptionConcurrency = 8
	maxTranscriptionRetries     = 10
)

type TranscriptionJob struct {
	Timeout       time.Duration
	MaxDuration   time.Duration
	MaxAudioBytes int64
	Concurrency   int
	ChunkRetries  int
}

func loadTranscriptionJob() (TranscriptionJob, error) {
	timeout, err := positiveDuration("TRANSCRIPTION_JOB_TIMEOUT", "2h")
	if err != nil {
		return TranscriptionJob{}, err
	}
	maxDuration, err := positiveDuration("TRANSCRIPTION_MAX_DURATION", "8h")
	if err != nil {
		return TranscriptionJob{}, err
	}
	maxBytes, err := positiveInt("TRANSCRIPTION_MAX_AUDIO_BYTES", "134217728")
	if err != nil {
		return TranscriptionJob{}, err
	}
	concurrency, err := boundedInt("TRANSCRIPTION_CONCURRENCY", "3", 1, maxTranscriptionConcurrency)
	if err != nil {
		return TranscriptionJob{}, err
	}
	retries, err := boundedInt("TRANSCRIPTION_CHUNK_RETRIES", "3", 0, maxTranscriptionRetries)
	if err != nil {
		return TranscriptionJob{}, err
	}
	return TranscriptionJob{
		Timeout:       timeout,
		MaxDuration:   maxDuration,
		MaxAudioBytes: maxBytes,
		Concurrency:   concurrency,
		ChunkRetries:  retries,
	}, nil
}

func boundedInt(key, fallback string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(envOr(key, fallback))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%s: must be from %d to %d, got %d", key, lo, hi, n)
	}
	return n, nil
}
