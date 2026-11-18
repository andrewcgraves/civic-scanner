//go:build integration

package migrations

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func isolatedProvider(t *testing.T) (*goose.Provider, *pgxpool.Pool, string) {
	t.Helper()
	ctx := t.Context()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Fatal("DATABASE_URL must be set for integration tests")
	}
	schema := "migrations_test_" + uuid.NewString()[:8]

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.WithoutCancel(ctx), `DROP SCHEMA `+schema+` CASCADE`)
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		t.Fatal(err)
	}
	return provider, pool, schema
}

func tableExists(t *testing.T, pool *pgxpool.Pool, schema, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, fmt.Sprintf("%s.%s", schema, table)).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestAgendaVersionsMigrationRollsBackAndReapplies(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !tableExists(t, pool, schema, "agenda_versions") {
		t.Fatal("agenda_versions missing after Up")
	}

	if _, err := provider.DownTo(ctx, 3); err != nil {
		t.Fatalf("DownTo(3): %v", err)
	}
	if tableExists(t, pool, schema, "agenda_versions") {
		t.Error("agenda_versions still exists after rolling back 00004")
	}
	if !tableExists(t, pool, schema, "meetings") {
		t.Error("meetings missing after rolling back only 00004")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if !tableExists(t, pool, schema, "agenda_versions") {
		t.Error("agenda_versions missing after reapplying")
	}
}

func TestRecordingsMigrationRollsBackAndReapplies(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)
	tables := []string{"recordings", "transcripts"}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, table := range tables {
		if !tableExists(t, pool, schema, table) {
			t.Fatalf("%s missing after Up", table)
		}
	}

	if _, err := provider.DownTo(ctx, 4); err != nil {
		t.Fatalf("DownTo(4): %v", err)
	}
	for _, table := range tables {
		if tableExists(t, pool, schema, table) {
			t.Errorf("%s still exists after rolling back 00005", table)
		}
	}
	if !tableExists(t, pool, schema, "agenda_versions") {
		t.Error("agenda_versions missing after rolling back only 00005")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	for _, table := range tables {
		if !tableExists(t, pool, schema, table) {
			t.Errorf("%s missing after reapplying", table)
		}
	}
}
