package config

import (
	"os"
	"time"
)

type Extract struct {
	PDFBinary    string
	PDFTimeout   time.Duration
	PDFMaxMemory int64
	MaxTextBytes int64
}

func loadExtract() (Extract, error) {
	timeout, err := positiveDuration("EXTRACT_PDF_TIMEOUT", "60s")
	if err != nil {
		return Extract{}, err
	}
	maxMemory, err := positiveInt("EXTRACT_PDF_MAX_MEMORY", "536870912")
	if err != nil {
		return Extract{}, err
	}
	maxText, err := positiveInt("EXTRACT_MAX_TEXT_BYTES", "8388608")
	if err != nil {
		return Extract{}, err
	}
	return Extract{
		PDFBinary:    os.Getenv("EXTRACT_PDF_BINARY"),
		PDFTimeout:   timeout,
		PDFMaxMemory: maxMemory,
		MaxTextBytes: maxText,
	}, nil
}
