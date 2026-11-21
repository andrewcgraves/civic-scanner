//go:build integration

package migrations

import "testing"

func TestAgencyScreeningsMigrationRollsBackAndReapplies(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !tableExists(t, pool, schema, "agency_screenings") {
		t.Fatal("agency_screenings missing after Up")
	}
	var agency string
	if err := pool.QueryRow(ctx, `INSERT INTO agencies (slug, name, status, website) VALUES ('screened', 'Screened', 'pending', 'https://screened.example.org') RETURNING id`).Scan(&agency); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agency_screenings (agency_id, status, verdict) VALUES ($1, 'complete', 'both')`, agency); err != nil {
		t.Fatalf("complete screening: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agency_screenings SET status = 'failed', verdict = NULL, failure = 'timeout' WHERE agency_id = $1`, agency); err != nil {
		t.Fatalf("failed screening: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agency_screenings SET evidence = '{}' WHERE agency_id = $1`, agency); err == nil {
		t.Error("evidence that is not an array was accepted")
	}

	if _, err := provider.DownTo(ctx, 17); err != nil {
		t.Fatalf("DownTo(17): %v", err)
	}
	if tableExists(t, pool, schema, "agency_screenings") {
		t.Error("agency_screenings still exists after rolling back 00018")
	}
	if !tableExists(t, pool, schema, "agency_submissions") {
		t.Error("agency_submissions missing after rolling back only 00018")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if !tableExists(t, pool, schema, "agency_screenings") {
		t.Error("agency_screenings missing after reapplying")
	}
}
