package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Fetch struct {
	UserAgent        string
	Timeout          time.Duration
	MaxBytes         int64
	MaxRedirects     int
	FirecrawlAPIKey  string
	FirecrawlBaseURL string
	FirecrawlTimeout time.Duration
}

func loadFetch() (Fetch, error) {
	timeout, err := positiveDuration("FETCH_TIMEOUT", "30s")
	if err != nil {
		return Fetch{}, err
	}
	firecrawlTimeout, err := positiveDuration("FIRECRAWL_TIMEOUT", "90s")
	if err != nil {
		return Fetch{}, err
	}
	maxBytes, err := positiveInt("FETCH_MAX_BYTES", "134217728")
	if err != nil {
		return Fetch{}, err
	}
	maxRedirects, err := positiveInt("FETCH_MAX_REDIRECTS", "5")
	if err != nil {
		return Fetch{}, err
	}
	return Fetch{
		UserAgent:        envOr("FETCH_USER_AGENT", "CivicScanner/1.0 (+https://github.com/andrewcgraves/civic-scanner)"),
		Timeout:          timeout,
		MaxBytes:         maxBytes,
		MaxRedirects:     int(maxRedirects),
		FirecrawlAPIKey:  os.Getenv("FIRECRAWL_API_KEY"),
		FirecrawlBaseURL: envOr("FIRECRAWL_BASE_URL", "https://api.firecrawl.dev"),
		FirecrawlTimeout: firecrawlTimeout,
	}, nil
}

func positiveDuration(key, fallback string) (time.Duration, error) {
	d, err := time.ParseDuration(envOr(key, fallback))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %s", key, d)
	}
	return d, nil
}

func positiveInt(key, fallback string) (int64, error) {
	n, err := strconv.ParseInt(envOr(key, fallback), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %d", key, n)
	}
	return n, nil
}
