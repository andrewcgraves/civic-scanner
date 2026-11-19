//go:build integration

package migrations

import "testing"

func TestAgendaChangeEventPendingMigrationRollsBackAndReapplies(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !columnExists(t, pool, schema, "agenda_versions", "change_event_pending") {
		t.Fatal("change_event_pending missing after Up")
	}

	if _, err := provider.DownTo(ctx, 10); err != nil {
		t.Fatalf("DownTo(10): %v", err)
	}
	if columnExists(t, pool, schema, "agenda_versions", "change_event_pending") {
		t.Error("change_event_pending still exists after rolling back 00011")
	}
	for _, table := range []string{"agenda_versions", "users"} {
		if !tableExists(t, pool, schema, table) {
			t.Errorf("%s missing after rolling back only 00011", table)
		}
	}
	for _, column := range []string{"download_attempts", "download_failure", "download_failed_at", "download_retry_after"} {
		if !columnExists(t, pool, schema, "recordings", column) {
			t.Errorf("recordings.%s missing after rolling back only 00011", column)
		}
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if !columnExists(t, pool, schema, "agenda_versions", "change_event_pending") {
		t.Error("change_event_pending missing after reapplying")
	}
}
