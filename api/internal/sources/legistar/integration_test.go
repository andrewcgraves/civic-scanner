//go:build integration

package legistar_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/legistar"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func checkAgencyWith(t *testing.T, cfg legistar.Config, fake *fetchtest.Fake, now time.Time) map[string]store.Meeting {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	pool := testdb.Pool(t)
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	slug := "legistar-test-" + hex.EncodeToString(suffix)

	var agencyID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, 'live', 'https://example.org') RETURNING id`,
		slug).Scan(&agencyID); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM meetings WHERE agency_id = $1`, agencyID)
		_, _ = pool.Exec(ctx, `DELETE FROM agencies WHERE id = $1`, agencyID)
	})

	clock := sourcestest.NewClock(now)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: clock},
		map[string]sources.Constructor{slug: legistar.Constructor(cfg)})
	queue := "legistar_test_" + hex.EncodeToString(suffix)
	client, err := jobs.NewClient(pool, slog.New(slog.DiscardHandler), jobs.Config{
		Registry:       registry,
		Clock:          clock,
		Enabled:        func(context.Context, string) bool { return true },
		Queue:          queue,
		NoPeriodicJobs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(ctx) })

	inserted, err := client.Insert(ctx, jobs.CheckAgencyArgs{AgencySlug: slug}, &river.InsertOpts{Queue: queue})
	if err != nil {
		t.Fatalf("insert check job: %v", err)
	}
	testdb.WaitForJobState(t, pool, inserted.Job.ID, "completed")

	page, err := store.New(pool).ListMeetings(ctx, agencyID, store.ListMeetingsParams{Limit: store.MaxMeetingsLimit})
	if err != nil {
		t.Fatalf("list meetings: %v", err)
	}
	stored := map[string]store.Meeting{}
	for _, m := range page.Meetings {
		stored[m.SourceID] = m
	}
	return stored
}

func TestCheckAgencyStoresLAMetroBoardMeetingsFromRecordedResponse(t *testing.T) {
	fake := serving(fixture(t, "metro_board_events.json"))
	stored := checkAgencyWith(t, legistar.LAMetro, fake, recordedAt)
	if len(stored) != 6 {
		t.Fatalf("stored %d meetings, want 6: %v", len(stored), stored)
	}
	want := map[string]struct {
		startsAt time.Time
		status   string
	}{
		"3549": {time.Date(2026, 10, 22, 17, 0, 0, 0, time.UTC), "held"},
		"3666": {time.Date(2027, 1, 20, 17, 0, 0, 0, time.UTC), "cancelled"},
		"3354": {time.Date(2026, 1, 22, 18, 0, 0, 0, time.UTC), "held"},
	}
	for id, w := range want {
		m := stored[id]
		if !m.StartsAt.Equal(w.startsAt) || m.Status != w.status || m.SourceURL != metroPage(id) || m.Title == "" {
			t.Errorf("meeting %s = %+v, want start %s, status %s, page %s", id, m, w.startsAt, w.status, metroPage(id))
		}
	}
	if calls := fake.Calls(); len(calls) != 1 {
		t.Errorf("requests = %d, want 1", len(calls))
	}
}

func TestCheckAgencyStoresOCTABoardMeetingsFromRecordedResponse(t *testing.T) {
	fake := serving(fixture(t, "octa_board_events.json"))
	stored := checkAgencyWith(t, legistar.OCTA, fake, time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC))
	if len(stored) != 7 {
		t.Fatalf("stored %d meetings, want 7: %v", len(stored), stored)
	}
	want := map[int]struct {
		startsAt time.Time
		status   string
	}{
		2900: {time.Date(2026, 8, 10, 16, 30, 0, 0, time.UTC), "cancelled"},
		2901: {time.Date(2026, 8, 24, 16, 30, 0, 0, time.UTC), "scheduled"},
		2906: {time.Date(2026, 11, 9, 17, 30, 0, 0, time.UTC), "scheduled"},
	}
	for id, w := range want {
		m := stored[strconv.Itoa(id)]
		if !m.StartsAt.Equal(w.startsAt) || m.Status != w.status || m.SourceURL != octaPage(id) || m.Title != "Board" {
			t.Errorf("meeting %d = %+v, want start %s, status %s, page %s", id, m, w.startsAt, w.status, octaPage(id))
		}
	}
}
