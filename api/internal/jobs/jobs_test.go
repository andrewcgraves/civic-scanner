package jobs

import (
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

func TestCheckAgencyArgsKind(t *testing.T) {
	if kind := (CheckAgencyArgs{}).Kind(); kind != "check_agency" {
		t.Errorf("Kind() = %q, want check_agency", kind)
	}
}

func TestNewClient(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://127.0.0.1:1/civic?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := NewClient(pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
}
