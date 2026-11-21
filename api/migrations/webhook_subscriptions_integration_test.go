//go:build integration

package migrations

import (
	"strings"
	"testing"
)

func TestWebhookSubscriptionsMigration(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.UpTo(ctx, 16); err != nil {
		t.Fatalf("UpTo(16): %v", err)
	}
	var userID, agencyID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_webhooks') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agencies (slug, name, status, website) VALUES ('webhook-agency', 'W', 'live', 'https://w.example') RETURNING id`).Scan(&agencyID); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO webhook_subscriptions (id, user_id, agency_id, url, payload_version, secret_ciphertext) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)`
	if _, err := pool.Exec(ctx, insert, userID, agencyID, "https://hooks.example/a", "v1", []byte{1}); err != nil {
		t.Fatalf("valid row: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM webhook_subscriptions WHERE url = 'https://hooks.example/a'`).Scan(&status); err != nil || status != "active" {
		t.Errorf("default status = %q, %v", status, err)
	}
	for name, tc := range map[string]struct {
		url        string
		version    string
		secret     []byte
		constraint string
	}{
		"duplicate":    {"https://hooks.example/a", "v1", []byte{1}, "webhook_subscriptions_user_agency_url_unique"},
		"http":         {"http://hooks.example/b", "v1", []byte{1}, "webhook_subscriptions_url_check"},
		"long url":     {"https://hooks.example/" + strings.Repeat("a", 2048), "v1", []byte{1}, "webhook_subscriptions_url_check"},
		"version":      {"https://hooks.example/c", "v2", []byte{1}, "webhook_subscriptions_payload_version_check"},
		"empty secret": {"https://hooks.example/d", "v1", []byte{}, "webhook_subscriptions_secret_ciphertext_check"},
	} {
		_, err := pool.Exec(ctx, insert, userID, agencyID, tc.url, tc.version, tc.secret)
		if _, constraint := pgCode(err); constraint != tc.constraint {
			t.Errorf("%s: err = %v, want %s", name, err, tc.constraint)
		}
	}
	_, err := pool.Exec(ctx, `UPDATE webhook_subscriptions SET status = 'disabled'`)
	if _, constraint := pgCode(err); constraint != "webhook_subscriptions_status_check" {
		t.Errorf("unknown status: err = %v", err)
	}

	if _, err := provider.DownTo(ctx, 15); err != nil {
		t.Fatalf("DownTo(15): %v", err)
	}
	if tableExists(t, pool, schema, "webhook_subscriptions") {
		t.Error("webhook_subscriptions still exists after rolling back 00016")
	}
	if !tableExists(t, pool, schema, "stripe_subscriptions") {
		t.Error("stripe_subscriptions missing after rolling back only 00016")
	}
	if _, err := provider.UpTo(ctx, 16); err != nil {
		t.Fatalf("UpTo(16) after Down: %v", err)
	}
	if !tableExists(t, pool, schema, "webhook_subscriptions") {
		t.Error("webhook_subscriptions missing after reapplying")
	}
}
