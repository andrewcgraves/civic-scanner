//go:build integration

package migrations_test

import (
	"context"
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

func TestSeedsMVPAgenciesAsLive(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)

	want := map[string]string{
		"metrolink": "https://metrolinktrains.com",
		"la-metro":  "https://www.metro.net",
		"vctc":      "https://www.goventura.org",
		"rctc":      "https://www.rctc.org",
		"sbcta":     "https://www.gosbcta.com",
		"octa":      "https://www.octa.net",
	}
	for slug, website := range want {
		var status, gotWebsite string
		if err := pool.QueryRow(ctx, `SELECT status, website FROM agencies WHERE slug = $1`, slug).Scan(&status, &gotWebsite); err != nil {
			t.Fatalf("agency %s: %v", slug, err)
		}
		if status != "live" || gotWebsite != website {
			t.Errorf("agency %s = (%s, %s), want (live, %s)", slug, status, gotWebsite, website)
		}
	}
}

func TestNewAgenciesDefaultToPending(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE slug = 'migration-test-default'`)
	})
	var status string
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name) VALUES ('migration-test-default', 'Default') RETURNING status`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Errorf("status = %q, want pending", status)
	}
	if _, err := pool.Exec(ctx, `UPDATE agencies SET status = 'bogus' WHERE slug = 'migration-test-default'`); err == nil {
		t.Error("status outside pending, live, rejected was accepted")
	}
}

func TestLiveAgencyRequiresWebsite(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE slug LIKE 'migration-test-website-%'`)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ('migration-test-website-live', 'Live', 'live', '')`); err == nil {
		t.Error("live agency with empty website was accepted")
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ('migration-test-website-pending', 'Pending', 'pending', '')`); err != nil {
		t.Errorf("pending agency with empty website was rejected: %v", err)
	}
}
