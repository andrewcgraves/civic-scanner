//go:build integration

package jobs_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/migrations"
)

func TestCheckAgencyJobRuns(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Fatal("DATABASE_URL must be set for integration tests")
	}
	ctx := context.WithoutCancel(t.Context())
	logger := slog.New(slog.DiscardHandler)

	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Up(ctx, pool, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	client, err := jobs.NewClient(pool, logger)
	if err != nil {
		t.Fatal(err)
	}
	completed, cancel := client.Subscribe(river.EventKindJobCompleted)
	t.Cleanup(cancel)
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(ctx) })

	inserted, err := client.Insert(ctx, jobs.CheckAgencyArgs{AgencySlug: "la-metro"}, nil)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}

	timeout := time.After(15 * time.Second)
	for {
		select {
		case ev := <-completed:
			if ev.Job.ID == inserted.Job.ID {
				return
			}
		case <-timeout:
			t.Fatal("job did not complete within 15s")
		}
	}
}
