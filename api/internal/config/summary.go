package config

import "fmt"

type Summary struct {
	AgendaTier string
}

func loadSummary() (Summary, error) {
	tier := envOr("AGENDA_SUMMARY_TIER", "routine")
	if tier != "routine" && tier != "strong" {
		return Summary{}, fmt.Errorf("AGENDA_SUMMARY_TIER: must be routine or strong, got %q", tier)
	}
	return Summary{AgendaTier: tier}, nil
}
