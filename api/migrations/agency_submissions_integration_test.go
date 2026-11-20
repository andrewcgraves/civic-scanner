//go:build integration

package migrations

import (
	"errors"
	"maps"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func constraintOf(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.ConstraintName
	}
	return ""
}

func TestAgencySubmissionsMigrationBackfillsSiteKeys(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.UpTo(ctx, 11); err != nil {
		t.Fatalf("UpTo(11): %v", err)
	}
	for _, row := range []struct{ slug, status, website string }{
		{"first", "pending", "HTTP://WWW.Example-A.org:8080/board?x=1"},
		{"duplicate", "rejected", "https://example-a.org"},
		{"bare-www", "pending", "https://www.com/"},
		{"no-website", "pending", ""},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, $2, $3)`, row.slug, row.status, row.website); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT a.slug, w.site_key FROM agency_websites w JOIN agencies a ON a.id = w.agency_id`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for rows.Next() {
		var slug, key string
		if err := rows.Scan(&slug, &key); err != nil {
			t.Fatal(err)
		}
		got[slug] = key
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"metrolink": "metrolinktrains.com", "la-metro": "metro.net", "vctc": "goventura.org",
		"rctc": "rctc.org", "sbcta": "gosbcta.com", "octa": "octa.net",
		"first": "example-a.org", "bare-www": "www.com",
	}
	if !maps.Equal(got, want) {
		t.Errorf("site keys = %v, want %v", got, want)
	}

	var agencyID, user string
	if err := pool.QueryRow(ctx, `SELECT id FROM agencies WHERE slug = 'no-website'`).Scan(&agencyID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agency_websites (agency_id, site_key) VALUES ($1, 'metro.net')`, agencyID); constraintOf(err) != "agency_websites_site_key_unique" {
		t.Errorf("duplicate site key: err = %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agency_websites (agency_id, site_key) VALUES ($1, '')`, agencyID); constraintOf(err) != "agency_websites_site_key_check" {
		t.Errorf("empty site key: err = %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_migration') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agency_submissions (agency_id, submitted_by) VALUES ($1, $2)`, agencyID, user); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user); constraintOf(err) == "" {
		t.Errorf("deleting a submitter: err = %v, want a foreign key violation", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM agencies WHERE id = $1`, agencyID); err != nil {
		t.Errorf("deleting a submitted agency: %v", err)
	}

	if _, err := provider.DownTo(ctx, 11); err != nil {
		t.Fatalf("DownTo(11): %v", err)
	}
	for _, table := range []string{"agency_websites", "agency_submissions"} {
		if tableExists(t, pool, schema, table) {
			t.Errorf("%s still exists after rolling back 00014", table)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if !tableExists(t, pool, schema, "agency_submissions") {
		t.Error("agency_submissions missing after reapplying")
	}
}
