package config

import "testing"

func TestLoadSummaryDefaultsToTheRoutineTier(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("AGENDA_SUMMARY_TIER", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Summary != (Summary{AgendaTier: "routine"}) {
		t.Errorf("Summary = %+v, want the routine tier", cfg.Summary)
	}
}

func TestLoadSummaryOverride(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("AGENDA_SUMMARY_TIER", "strong")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Summary.AgendaTier != "strong" {
		t.Errorf("AgendaTier = %q, want strong", cfg.Summary.AgendaTier)
	}
}

func TestLoadSummaryErrors(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, value := range []string{"premium", "Routine", "anthropic/claude-haiku-5.5"} {
		t.Setenv("AGENDA_SUMMARY_TIER", value)
		if _, err := Load(); err == nil {
			t.Errorf("AGENDA_SUMMARY_TIER=%s: expected an error", value)
		}
	}
}
