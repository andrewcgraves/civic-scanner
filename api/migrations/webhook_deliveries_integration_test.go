//go:build integration

package migrations

import (
	"strings"
	"testing"
)

func TestWebhookDeliveriesMigration(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.UpTo(ctx, 16); err != nil {
		t.Fatalf("UpTo(16): %v", err)
	}
	var userID, agencyID, meetingID, oldEvent string
	if err := pool.QueryRow(ctx, `INSERT INTO users (workos_user_id) VALUES ('user_deliveries') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agencies (slug, name, status, website) VALUES ('deliveries', 'D', 'live', 'https://d.example') RETURNING id`).Scan(&agencyID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO meetings (agency_id, title, starts_at, status, source_id) VALUES ($1, 'Board', now(), 'scheduled', 'm1') RETURNING id`, agencyID).Scan(&meetingID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO meeting_events (type, meeting_id) VALUES ('agenda_published', $1) RETURNING id`, meetingID).Scan(&oldEvent); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.UpTo(ctx, 19); err != nil {
		t.Fatalf("UpTo(19): %v", err)
	}
	var backfilled int
	if err := pool.QueryRow(ctx, `SELECT deliveries FROM webhook_event_fan_outs WHERE event_id = $1`, oldEvent).Scan(&backfilled); err != nil || backfilled != 0 {
		t.Errorf("existing event is not marked as fanned out: %d, %v", backfilled, err)
	}

	var subID, newEvent string
	if err := pool.QueryRow(ctx, `INSERT INTO webhook_subscriptions (id, user_id, agency_id, url, payload_version, secret_ciphertext)
VALUES (gen_random_uuid(), $1, $2, 'https://hooks.example/a', 'v1', '\x01') RETURNING id`, userID, agencyID).Scan(&subID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO meeting_events (type, meeting_id) VALUES ('agenda_changed', $1) RETURNING id`, meetingID).Scan(&newEvent); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO webhook_event_fan_outs (event_id, event_sequence, deliveries) VALUES ($1, 2, -1)`, newEvent); err == nil {
		t.Error("negative deliveries accepted")
	} else if _, constraint := pgCode(err); constraint != "webhook_event_fan_outs_deliveries_check" {
		t.Errorf("negative deliveries: %v", err)
	}

	insert := `INSERT INTO webhook_delivery_attempts (subscription_id, event_id, attempt, started_at, duration_ms, status_code, error, succeeded)
VALUES ($1, $2, $3, now(), $4, $5, $6, $7)`
	for name, row := range map[string][]any{
		"success":      {1, 12, 204, nil, true},
		"http failure": {1, 12, 500, nil, false},
		"no answer":    {2, 15000, nil, "timeout", false},
	} {
		if _, err := pool.Exec(ctx, insert, append([]any{subID, newEvent}, row...)...); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, tc := range map[string]struct {
		row        []any
		constraint string
	}{
		"attempt zero":       {[]any{0, 1, 204, nil, true}, "webhook_delivery_attempts_attempt_check"},
		"negative duration":  {[]any{1, -1, 204, nil, true}, "webhook_delivery_attempts_duration_check"},
		"bad status":         {[]any{1, 1, 99, nil, false}, "webhook_delivery_attempts_status_code_check"},
		"empty error":        {[]any{1, 1, nil, "", false}, "webhook_delivery_attempts_error_check"},
		"long error":         {[]any{1, 1, nil, strings.Repeat("x", 501), false}, "webhook_delivery_attempts_error_check"},
		"success with 500":   {[]any{1, 1, 500, nil, true}, "webhook_delivery_attempts_outcome_check"},
		"success with error": {[]any{1, 1, 204, "x", true}, "webhook_delivery_attempts_outcome_check"},
		"failure with none":  {[]any{1, 1, nil, nil, false}, "webhook_delivery_attempts_outcome_check"},
	} {
		_, err := pool.Exec(ctx, insert, append([]any{subID, newEvent}, tc.row...)...)
		if _, constraint := pgCode(err); constraint != tc.constraint {
			t.Errorf("%s: err = %v, want %s", name, err, tc.constraint)
		}
	}

	if _, err := pool.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, subID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM webhook_delivery_attempts`).Scan(&left); err != nil || left != 0 {
		t.Errorf("attempts left after deleting the subscription = %d, %v", left, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM meeting_events WHERE id = $1`, oldEvent); err != nil {
		t.Errorf("an event that was fanned out can still be deleted: %v", err)
	}

	if _, err := provider.DownTo(ctx, 18); err != nil {
		t.Fatalf("DownTo(18): %v", err)
	}
	for _, table := range []string{"webhook_delivery_attempts", "webhook_event_fan_outs"} {
		if tableExists(t, pool, schema, table) {
			t.Errorf("%s still exists after rolling back 00019", table)
		}
	}
	if !tableExists(t, pool, schema, "webhook_subscriptions") || !tableExists(t, pool, schema, "meeting_events") {
		t.Error("rolling back 00019 dropped an earlier table")
	}
}
