//go:build integration

package migrations

import (
	"testing"
	"time"
)

func TestWebhookSubscriptionPauseMigration(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.UpTo(ctx, 19); err != nil {
		t.Fatalf("UpTo(19): %v", err)
	}
	var userID, agencyID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_pause') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agencies (slug, name, status, website) VALUES ('pause', 'P', 'live', 'https://p.example') RETURNING id`).Scan(&agencyID); err != nil {
		t.Fatal(err)
	}
	pausedAt := time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)
	insert := `INSERT INTO webhook_subscriptions (id, user_id, agency_id, url, payload_version, status, secret_ciphertext, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, 'v1', $4, '\x01', $5) RETURNING id`
	var activeID, pausedID string
	if err := pool.QueryRow(ctx, insert, userID, agencyID, "https://hooks.example/active", "active", pausedAt).Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, insert, userID, agencyID, "https://hooks.example/paused", "paused", pausedAt).Scan(&pausedID); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.UpTo(ctx, 20); err != nil {
		t.Fatalf("UpTo(20): %v", err)
	}
	read := `SELECT consecutive_failed_events, paused_reason, paused_at FROM webhook_subscriptions WHERE id = $1`
	var (
		failures int
		reason   *string
		at       *time.Time
	)
	if err := pool.QueryRow(ctx, read, activeID).Scan(&failures, &reason, &at); err != nil || failures != 0 || reason != nil || at != nil {
		t.Errorf("active row = %d, %v, %v, %v", failures, reason, at, err)
	}
	if err := pool.QueryRow(ctx, read, pausedID).Scan(&failures, &reason, &at); err != nil || failures != 0 || reason == nil || *reason != "owner" ||
		at == nil || !at.Equal(pausedAt) {
		t.Errorf("paused row = %d, %v, %v, %v", failures, reason, at, err)
	}
	for name, tc := range map[string]struct {
		set        string
		constraint string
	}{
		"reason":   {`paused_reason = 'tired'`, "webhook_subscriptions_paused_reason_check"},
		"negative": {`consecutive_failed_events = -1`, "webhook_subscriptions_consecutive_failed_events_check"},
	} {
		_, err := pool.Exec(ctx, `UPDATE webhook_subscriptions SET `+tc.set+` WHERE id = $1`, activeID)
		if _, constraint := pgCode(err); constraint != tc.constraint {
			t.Errorf("%s: err = %v, want %s", name, err, tc.constraint)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE webhook_subscriptions SET paused_reason = 'failed_deliveries', consecutive_failed_events = 5 WHERE id = $1`, activeID); err != nil {
		t.Errorf("valid pause: %v", err)
	}

	if _, err := provider.DownTo(ctx, 19); err != nil {
		t.Fatalf("DownTo(19): %v", err)
	}
	for _, column := range []string{"consecutive_failed_events", "paused_reason", "paused_at"} {
		if columnExists(t, pool, schema, "webhook_subscriptions", column) {
			t.Errorf("%s still exists after rolling back 00020", column)
		}
	}
	if _, err := provider.UpTo(ctx, 20); err != nil {
		t.Fatalf("UpTo(20) after Down: %v", err)
	}
}
