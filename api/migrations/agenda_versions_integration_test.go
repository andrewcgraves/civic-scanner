//go:build integration

package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

const validHash = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

func insertAgendaMeeting(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	ctx := t.Context()
	agencyID := insertMeetingAgency(t, pool, slug)
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO meetings (agency_id, title, starts_at, source_id) VALUES ($1, 't', now(), $2) RETURNING id`,
		agencyID, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agenda_versions WHERE meeting_id = $1`, id)
	})
	return id
}

func insertAgendaVersion(ctx context.Context, pool *pgxpool.Pool, meetingID string, version int, sourceURL, hash string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO agenda_versions (meeting_id, version, source_url, extracted_text, content_hash) VALUES ($1, $2, $3, 'text', $4)`,
		meetingID, version, sourceURL, hash)
	return err
}

func TestAgendaVersionsDefaults(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertAgendaMeeting(t, pool, "migration-test-agenda-defaults")

	var (
		summary, changeNote *string
		createdSet, updated bool
	)
	if err := pool.QueryRow(ctx, `
INSERT INTO agenda_versions (meeting_id, version, source_url, extracted_text, content_hash)
VALUES ($1, 1, 'https://example.com/a.pdf', 'text', $2)
RETURNING summary, change_note, created_at IS NOT NULL, updated_at IS NOT NULL`,
		meetingID, validHash).Scan(&summary, &changeNote, &createdSet, &updated); err != nil {
		t.Fatal(err)
	}
	if summary != nil || changeNote != nil || !createdSet || !updated {
		t.Errorf("summary = %v, change_note = %v, timestamps set = %v %v; want null, null, true, true", summary, changeNote, createdSet, updated)
	}
}

func TestAgendaVersionsAreUniquePerMeeting(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	first := insertAgendaMeeting(t, pool, "migration-test-agenda-unique-a")
	second := insertAgendaMeeting(t, pool, "migration-test-agenda-unique-b")

	if err := insertAgendaVersion(ctx, pool, first, 1, "https://example.com/a.pdf", validHash); err != nil {
		t.Fatal(err)
	}
	if err := insertAgendaVersion(ctx, pool, first, 1, "https://example.com/a.pdf", validHash); err == nil {
		t.Error("duplicate (meeting_id, version) was accepted")
	}
	if err := insertAgendaVersion(ctx, pool, second, 1, "https://example.com/a.pdf", validHash); err != nil {
		t.Errorf("version 1 for another meeting was rejected: %v", err)
	}
}

func TestAgendaVersionsRejectInvalidRows(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	meetingID := insertAgendaMeeting(t, pool, "migration-test-agenda-invalid")

	for name, tc := range map[string]struct {
		meetingID string
		version   int
		sourceURL string
		hash      string
	}{
		"version zero":       {meetingID, 0, "https://example.com/a.pdf", validHash},
		"empty source url":   {meetingID, 1, "", validHash},
		"short hash":         {meetingID, 1, "https://example.com/a.pdf", "abc"},
		"uppercase hash":     {meetingID, 1, "https://example.com/a.pdf", strings.ToUpper(validHash)},
		"missing meeting id": {"00000000-0000-0000-0000-000000000001", 1, "https://example.com/a.pdf", validHash},
	} {
		if err := insertAgendaVersion(ctx, pool, tc.meetingID, tc.version, tc.sourceURL, tc.hash); err == nil {
			t.Errorf("%s: row was accepted", name)
		}
	}
}
