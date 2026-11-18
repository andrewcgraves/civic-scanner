//go:build integration

package jobs_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestCheckAgencyJobRuns(t *testing.T) {
	ctx := context.WithoutCancel(t.Context())
	pool := testdb.Pool(t)

	client, err := jobs.NewClient(pool, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(ctx) })

	inserted, err := client.Insert(ctx, jobs.CheckAgencyArgs{AgencySlug: "la-metro"}, nil)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	testdb.WaitForJobState(t, pool, inserted.Job.ID, "completed")
}
