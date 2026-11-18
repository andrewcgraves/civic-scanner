//go:build integration

package migrations_test

import (
	"log/slog"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/migrations"
)

func TestUpIsIdempotent(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)

	if err := migrations.Up(ctx, pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	for _, table := range []string{"agencies", "goose_db_version", "river_job"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s does not exist after Up", table)
		}
	}
}
