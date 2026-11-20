//go:build integration

package store_test

import (
	"maps"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestLatestAgendaSourceURLsReadsTheLatestVersionOfTheAgencysMeetings(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-agenda-source-urls")
	otherID := seedAgency(t, pool, "store-test-agenda-source-urls-other")
	start := time.Date(2026, 11, 3, 18, 0, 0, 0, time.UTC)
	several := seedMeetingAt(t, pool, agencyID, "several", start)
	single := seedMeetingAt(t, pool, agencyID, "single", start.AddDate(0, 0, 7))
	seedMeetingAt(t, pool, agencyID, "none", start.AddDate(0, 0, 14))
	unasked := seedMeetingAt(t, pool, agencyID, "unasked", start.AddDate(0, 0, 21))
	elsewhere := seedMeetingAt(t, pool, otherID, "several", start)

	add := func(in store.AgendaVersionInput, url string) {
		t.Helper()
		in.SourceURL = url
		if _, created, err := s.AddAgendaVersion(ctx, in); err != nil || !created {
			t.Fatalf("add %s = (created %v, %v)", url, created, err)
		}
	}
	add(agendaInput(several, "first"), "https://agency.test/several/1.pdf")
	add(agendaInput(several, "second"), "https://agency.test/several/2.pdf")
	add(agendaInput(single, "only"), "https://agency.test/single.pdf")
	add(agendaInput(unasked, "unasked"), "https://agency.test/unasked.pdf")
	add(agendaInput(elsewhere, "elsewhere"), "https://other.test/several.pdf")

	var got map[string]string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		got, err = s.LatestAgendaSourceURLsTx(ctx, tx, agencyID, []string{"several", "single", "none", "missing"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"several": "https://agency.test/several/2.pdf", "single": "https://agency.test/single.pdf"}
	if !maps.Equal(got, want) {
		t.Errorf("latest source urls = %v, want %v", got, want)
	}

	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		got, err = s.LatestAgendaSourceURLsTx(ctx, tx, agencyID, nil)
		return err
	}); err != nil || len(got) != 0 {
		t.Errorf("no source ids = (%v, %v), want an empty map", got, err)
	}
}

func TestTheSameTextFromAnotherURLMovesTheLatestVersion(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-move-url")

	first, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "1. Call to Order"))
	if err != nil {
		t.Fatal(err)
	}
	summarized, err := s.SetAgendaSummary(ctx, first.ID, "The board meets.")
	if err != nil {
		t.Fatal(err)
	}
	events := eventTypes(t, s, meetingID)

	same, created, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "1. Call to Order"))
	if err != nil || created || same.ID != first.ID || same.SourceURL != first.SourceURL || !same.UpdatedAt.Equal(summarized.UpdatedAt) {
		t.Errorf("same text and url = (%+v, created %v, %v), want a no-op", same, created, err)
	}

	moved := agendaInput(meetingID, "1. Call to Order")
	moved.SourceURL = "https://agency.test/moved/agenda.pdf"
	got, created, err := s.AddAgendaVersion(ctx, moved)
	if err != nil || created || got.RefreshedInPlace {
		t.Fatalf("same text from another url = (created %v, refreshed %v, %v), want neither", created, got.RefreshedInPlace, err)
	}
	if got.ID != first.ID || got.Version != 1 || got.SourceURL != moved.SourceURL || got.ContentHash != first.ContentHash ||
		got.Summary == nil || *got.Summary != "The board meets." || !got.CreatedAt.Equal(first.CreatedAt) || !got.UpdatedAt.After(summarized.UpdatedAt) {
		t.Errorf("moved version = %+v, want version 1 with the new url, its summary kept", got)
	}

	versions, err := s.ListAgendaVersions(ctx, meetingID)
	if err != nil || len(versions) != 1 || versions[0].SourceURL != moved.SourceURL {
		t.Errorf("versions = %+v (%v), want the one version at the new url", versions, err)
	}
	if after := eventTypes(t, s, meetingID); len(after) != len(events) {
		t.Errorf("events = %v, want still %v", after, events)
	}
}
