//go:build integration

package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestRunWorksJobsUntilCanceled(t *testing.T) {
	t.Setenv("DATABASE_URL", testdb.URL(t))
	t.Setenv("LOG_LEVEL", "error")
	pool := testdb.Pool(t)

	inserter, err := jobs.NewClient(pool, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := inserter.Insert(t.Context(), jobs.CheckAgencyArgs{AgencySlug: "worker-test"}, nil)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	testdb.WaitForJobState(t, pool, inserted.Job.ID, "completed")
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after the context was canceled")
	}
}
