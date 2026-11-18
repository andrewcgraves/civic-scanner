//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func insertMeetingAgency(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	ctx := t.Context()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM meetings WHERE agency_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM agencies WHERE id = $1`, id)
	})
	return id
}

func TestMeetingsRejectUnknownStatus(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	agencyID := insertMeetingAgency(t, pool, "migration-test-meeting-status")

	for _, status := range []string{"scheduled", "held", "cancelled"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO meetings (agency_id, title, starts_at, status, source_url, source_id) VALUES ($1, 't', now(), $2, '', $2)`,
			agencyID, status); err != nil {
			t.Errorf("status %q rejected: %v", status, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO meetings (agency_id, title, starts_at, status, source_url, source_id) VALUES ($1, 't', now(), 'bogus', '', 'bogus')`,
		agencyID); err == nil {
		t.Error("status outside scheduled, held, cancelled was accepted")
	}
}

func TestMeetingsAreUniquePerAgencySourceID(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	first := insertMeetingAgency(t, pool, "migration-test-meeting-unique-a")
	second := insertMeetingAgency(t, pool, "migration-test-meeting-unique-b")

	insert := `INSERT INTO meetings (agency_id, title, starts_at, status, source_url, source_id) VALUES ($1, 't', now(), 'scheduled', '', 'same')`
	if _, err := pool.Exec(ctx, insert, first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, first); err == nil {
		t.Error("duplicate (agency_id, source_id) was accepted")
	}
	if _, err := pool.Exec(ctx, insert, second); err != nil {
		t.Errorf("same source_id for another agency was rejected: %v", err)
	}
}

func TestMeetingsRequireExistingAgency(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)

	if _, err := pool.Exec(ctx,
		`INSERT INTO meetings (agency_id, title, starts_at, status, source_url, source_id) VALUES (gen_random_uuid(), 't', now(), 'scheduled', '', 'orphan')`); err == nil {
		t.Error("meeting for a missing agency was accepted")
	}
}

func TestMeetingsHaveAgencyNewestFirstIndex(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)

	var def string
	if err := pool.QueryRow(ctx,
		`SELECT indexdef FROM pg_indexes WHERE tablename = 'meetings' AND indexdef LIKE '%agency_id, starts_at DESC, id DESC%'`).Scan(&def); err != nil {
		t.Fatalf("no index on (agency_id, starts_at DESC, id DESC): %v", err)
	}
}
