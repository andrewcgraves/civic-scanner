//go:build integration

package migrations

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func columnExists(t *testing.T, pool *pgxpool.Pool, schema, table, column string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(t.Context(), `
SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3)`,
		schema, table, column).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestAgendaChangeEventPendingMigrationRollsBackAndReapplies(t *testing.T) {
	ctx := t.Context()
	provider, pool, schema := isolatedProvider(t)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !columnExists(t, pool, schema, "agenda_versions", "change_event_pending") {
		t.Fatal("change_event_pending missing after Up")
	}

	if _, err := provider.DownTo(ctx, 9); err != nil {
		t.Fatalf("DownTo(9): %v", err)
	}
	if columnExists(t, pool, schema, "agenda_versions", "change_event_pending") {
		t.Error("change_event_pending still exists after rolling back 00011")
	}
	for _, table := range []string{"agenda_versions", "users"} {
		if !tableExists(t, pool, schema, table) {
			t.Errorf("%s missing after rolling back only 00011 in %s", table, schema)
		}
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
	if !columnExists(t, pool, schema, "agenda_versions", "change_event_pending") {
		t.Error("change_event_pending missing after reapplying")
	}
}
