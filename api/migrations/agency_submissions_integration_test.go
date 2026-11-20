//go:build integration

package migrations

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type noticeLog struct {
	mu       sync.Mutex
	messages []string
}

func (n *noticeLog) configure(cfg *pgxpool.Config) {
	cfg.ConnConfig.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.messages = append(n.messages, notice.Message)
	}
}

func (n *noticeLog) matching(substr string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, m := range n.messages {
		if strings.Contains(m, substr) {
			out = append(out, m)
		}
	}
	return out
}

type seededAgency struct {
	slug, status, website string
	ageMinutes            int
}

func seedBeforeSubmissions(t *testing.T, pool *pgxpool.Pool, rows []seededAgency) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, row := range rows {
		var id string
		if err := pool.QueryRow(t.Context(),
			`INSERT INTO agencies (slug, name, status, website, created_at) VALUES ($1, $1, $2, $3, now() - make_interval(mins => $4)) RETURNING id`,
			row.slug, row.status, row.website, row.ageMinutes).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[row.slug] = id
	}
	return ids
}

func constraintOf(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.ConstraintName
	}
	return ""
}

func TestAgencySubmissionsMigrationBackfillsSiteKeys(t *testing.T) {
	ctx := t.Context()
	notices := &noticeLog{}
	provider, pool, schema := isolatedProvider(t, notices.configure)

	if _, err := provider.UpTo(ctx, 11); err != nil {
		t.Fatalf("UpTo(11): %v", err)
	}
	ids := seedBeforeSubmissions(t, pool, []seededAgency{
		{"first", "pending", "HTTP://WWW.Example-A.org:8080/board?x=1", 50},
		{"duplicate", "rejected", "https://example-a.org", 40},
		{"old-pending", "pending", "https://www.example-b.org", 30},
		{"new-live", "live", "https://example-b.org/meetings", 1},
		{"bare-www", "pending", "https://www.com/", 20},
		{"no-website", "pending", "", 10},
	})
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
		"first": "example-a.org", "new-live": "example-b.org", "bare-www": "www.com",
	}
	if !maps.Equal(got, want) {
		t.Errorf("site keys = %v, want %v", got, want)
	}
	for _, loser := range []struct{ slug, holder, status string }{
		{"duplicate", "first", "rejected"},
		{"old-pending", "new-live", "pending"},
	} {
		found := notices.matching("agency " + ids[loser.slug] + " left without a site row")
		if len(found) != 1 || !strings.Contains(found[0], ids[loser.holder]) || !strings.HasPrefix(found[0], "agency_websites backfill: "+loser.status) {
			t.Errorf("notice for %s = %q, want one naming %s", loser.slug, found, loser.holder)
		}
	}
	if extra := notices.matching("agency_websites backfill"); len(extra) != 2 {
		t.Errorf("backfill notices = %q, want exactly the two losers", extra)
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

func TestAgencySubmissionsMigrationRefusesLiveAgenciesSharingASite(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.UpTo(ctx, 11); err != nil {
		t.Fatalf("UpTo(11): %v", err)
	}
	ids := seedBeforeSubmissions(t, pool, []seededAgency{
		{"metro-copy", "live", "http://WWW.METRO.NET/board", 5},
		{"other-copy", "pending", "https://metro.net", 4},
	})
	_, err := provider.Up(ctx)
	if err == nil {
		t.Fatal("Up succeeded with two live agencies on one site")
	}
	for _, want := range []string{"live agencies", ids["metro-copy"], "metro.net", "fix their websites"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if slices.Contains([]bool{tableExists(t, pool, schema, "agency_websites"), tableExists(t, pool, schema, "agency_submissions")}, true) {
		t.Error("a failed backfill left tables behind")
	}
}
