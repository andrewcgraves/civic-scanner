package jobs

import (
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestCheckAgencyWorker(t *testing.T) {
	w := &CheckAgencyWorker{logger: slog.New(slog.DiscardHandler)}
	job := &river.Job[CheckAgencyArgs]{
		JobRow: &rivertype.JobRow{ID: 1},
		Args:   CheckAgencyArgs{AgencySlug: "la-metro"},
	}
	if err := w.Work(t.Context(), job); err != nil {
		t.Fatalf("Work: %v", err)
	}
}
