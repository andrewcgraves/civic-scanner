//go:build integration

package granicus_test

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

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/granicus"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestCheckAgencyStoresMetrolinkBoardMeetingsFromRecordedFeeds(t *testing.T) {
	ctx := context.WithoutCancel(t.Context())
	pool := testdb.Pool(t)
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	slug := "granicus-test-" + hex.EncodeToString(suffix)

	var agencyID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, 'live', 'https://metrolinktrains.com') RETURNING id`,
		slug).Scan(&agencyID); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM meetings WHERE agency_id = $1`, agencyID)
		_, _ = pool.Exec(ctx, `DELETE FROM agencies WHERE id = $1`, agencyID)
	})

	fake := recorded(t)
	clock := sourcestest.NewClock(recordedAt)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: clock},
		map[string]sources.Constructor{slug: granicus.Constructor(granicus.Metrolink)})
	queue := "granicus_test_" + hex.EncodeToString(suffix)
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
	if len(stored) != 2 {
		t.Fatalf("stored %d meetings, want 2: %v", len(stored), stored)
	}
	want := map[int]time.Time{
		1436: time.Date(2026, 10, 23, 17, 15, 0, 0, time.UTC),
		1438: time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC),
	}
	for clip, startsAt := range want {
		m := stored[strconv.Itoa(clip)]
		if !m.StartsAt.Equal(startsAt) || m.Status != "held" || m.SourceURL != mediaPlayer(clip) || m.Title != "Board of Directors" {
			t.Errorf("meeting %d = %+v, want start %s, held, page %s", clip, m, startsAt, mediaPlayer(clip))
		}
	}
	if calls := fake.Calls(); len(calls) != 2 {
		t.Errorf("requests = %d, want 2", len(calls))
	}
}
