//go:build integration

package migrations_test

import (
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestAgendaVersionsAreNotPendingByDefault(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertAgendaMeeting(t, pool, "migration-test-agenda-pending")

	var pending bool
	if err := pool.QueryRow(ctx, `
INSERT INTO agenda_versions (meeting_id, version, source_url, extracted_text, content_hash)
VALUES ($1, 1, 'https://example.com/a.pdf', 'text', $2)
RETURNING change_event_pending`, meetingID, validHash).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Error("change_event_pending = true for a row inserted without it, want false")
	}
}
