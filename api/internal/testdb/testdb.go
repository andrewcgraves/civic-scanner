package testdb

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/migrations"
)

func URL(t testing.TB) string {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Fatal("DATABASE_URL must be set for integration tests")
	}
	return url
}

func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	pool, err := store.Open(ctx, URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Up(ctx, pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func WaitForJobState(t testing.TB, pool *pgxpool.Pool, jobID int64, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(t.Context(), `SELECT state FROM river_job WHERE id = $1`, jobID).Scan(&state); err != nil {
			t.Fatalf("query job %d: %v", jobID, err)
		}
		if state == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %d state = %q after 15s, want %q", jobID, state, want)
}
