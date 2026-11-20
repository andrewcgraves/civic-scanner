package config

import (
	"fmt"
	"strconv"
	"time"
)

const DefaultAgendaRefreshGrace = 48 * time.Hour

type Ingest struct {
	CheckHourUTC       int
	AgendaRefreshGrace time.Duration
}

func loadIngest() (Ingest, error) {
	hour, err := strconv.Atoi(envOr("INGEST_CHECK_HOUR_UTC", "13"))
	if err != nil {
		return Ingest{}, fmt.Errorf("INGEST_CHECK_HOUR_UTC: %w", err)
	}
	if hour < 0 || hour > 23 {
		return Ingest{}, fmt.Errorf("INGEST_CHECK_HOUR_UTC: must be 0 to 23, got %d", hour)
	}
	grace, err := positiveDuration("INGEST_AGENDA_REFRESH_GRACE", DefaultAgendaRefreshGrace.String())
	if err != nil {
		return Ingest{}, err
	}
	return Ingest{CheckHourUTC: hour, AgendaRefreshGrace: grace}, nil
}
