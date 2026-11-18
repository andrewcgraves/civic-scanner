package config

import (
	"fmt"
	"strconv"
)

type Ingest struct {
	CheckHourUTC int
}

func loadIngest() (Ingest, error) {
	hour, err := strconv.Atoi(envOr("INGEST_CHECK_HOUR_UTC", "13"))
	if err != nil {
		return Ingest{}, fmt.Errorf("INGEST_CHECK_HOUR_UTC: %w", err)
	}
	if hour < 0 || hour > 23 {
		return Ingest{}, fmt.Errorf("INGEST_CHECK_HOUR_UTC: must be 0 to 23, got %d", hour)
	}
	return Ingest{CheckHourUTC: hour}, nil
}
