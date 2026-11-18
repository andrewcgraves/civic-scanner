//go:build integration

package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestGetLiveAgencyFindsOnlyLiveAgencies(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)

	for _, row := range []struct{ slug, status string }{
		{"store-test-get-live", "live"},
		{"store-test-get-pending", "pending"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, $2, $3) ON CONFLICT (slug) DO NOTHING`,
			row.slug, row.status, "https://"+row.slug+".example.com"); err != nil {
			t.Fatalf("insert agency: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE slug LIKE 'store-test-get-%'`)
	})
	s := store.New(pool)

	got, err := s.GetLiveAgency(ctx, "store-test-get-live")
	if err != nil {
		t.Fatalf("GetLiveAgency: %v", err)
	}
	if got.Slug != "store-test-get-live" || got.ID == uuid.Nil || got.Website != "https://store-test-get-live.example.com" {
		t.Errorf("agency = %+v", got)
	}
	for _, slug := range []string{"store-test-get-pending", "store-test-get-missing"} {
		if _, err := s.GetLiveAgency(ctx, slug); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetLiveAgency(%s) = %v, want ErrNotFound", slug, err)
		}
	}
}

func TestGetLiveAgencyFailsOnClosedPool(t *testing.T) {
	pool, err := store.Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()

	_, err = store.New(pool).GetLiveAgency(t.Context(), "la-metro")
	if err == nil || errors.Is(err, store.ErrNotFound) || !strings.HasPrefix(err.Error(), "get agency:") {
		t.Fatalf("err = %v, want get agency error", err)
	}
}

func TestUpsertMeetingNeverMovesAHeldMeetingBack(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-upsert-held")
	start := time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC)

	in := meetingInput(agencyID, "m-held", start)
	in.Status = "held"
	if _, _, err := s.UpsertMeeting(ctx, in); err != nil {
		t.Fatalf("UpsertMeeting: %v", err)
	}
	for _, reported := range []string{"scheduled", "cancelled"} {
		in.Status = reported
		in.Title = "Board Meeting (" + reported + ")"
		got, _, err := s.UpsertMeeting(ctx, in)
		if err != nil {
			t.Fatalf("UpsertMeeting(%s): %v", reported, err)
		}
		if got.Status != "held" || got.Title != in.Title {
			t.Errorf("reported %s: status = %q title = %q, want held with the new title", reported, got.Status, got.Title)
		}
	}
}

func TestUpsertMeetingFollowsOtherStatusChanges(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "store-test-upsert-status")
	in := meetingInput(agencyID, "m-status", time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC))

	for _, status := range []string{"scheduled", "cancelled", "scheduled", "held"} {
		in.Status = status
		got, _, err := s.UpsertMeeting(ctx, in)
		if err != nil {
			t.Fatalf("UpsertMeeting(%s): %v", status, err)
		}
		if got.Status != status {
			t.Errorf("status = %q, want %q", got.Status, status)
		}
	}
}
