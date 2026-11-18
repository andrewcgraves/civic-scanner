//go:build integration

package granicus_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/granicus"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type harness struct {
	t        *testing.T
	pool     *pgxpool.Pool
	client   *river.Client[pgx.Tx]
	clock    *sourcestest.Clock
	queue    string
	agencyID uuid.UUID
	slug     string
}

func newHarness(t *testing.T, fetcher fetch.Fetcher) *harness {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	pool := testdb.Pool(t)
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, pool: pool, slug: "granicus-test-" + hex.EncodeToString(suffix), queue: "granicus_test_" + hex.EncodeToString(suffix)}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, 'live', 'https://metrolinktrains.com') RETURNING id`,
		h.slug).Scan(&h.agencyID); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM meetings WHERE agency_id = $1`, h.agencyID)
		_, _ = pool.Exec(ctx, `DELETE FROM agencies WHERE id = $1`, h.agencyID)
	})

	h.clock = sourcestest.NewClock(recordedAt)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fetcher, Clock: h.clock},
		map[string]sources.Constructor{h.slug: granicus.Constructor(granicus.Metrolink)})
	client, err := jobs.NewClient(pool, slog.New(slog.DiscardHandler), jobs.Config{
		Registry:       registry,
		Clock:          h.clock,
		Enabled:        func(context.Context, string) bool { return true },
		Queue:          h.queue,
		NoPeriodicJobs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(ctx) })
	h.client = client
	return h
}

func (h *harness) check(now time.Time) {
	h.t.Helper()
	h.clock.Set(now)
	inserted, err := h.client.Insert(context.WithoutCancel(h.t.Context()), jobs.CheckAgencyArgs{AgencySlug: h.slug},
		&river.InsertOpts{Queue: h.queue, ScheduledAt: now})
	if err != nil {
		h.t.Fatalf("insert check job: %v", err)
	}
	if inserted.UniqueSkippedAsDuplicate {
		h.t.Fatalf("check on %s skipped as a duplicate", now)
	}
	testdb.WaitForJobState(h.t, h.pool, inserted.Job.ID, "completed")
}

func (h *harness) meetings() map[string]store.Meeting {
	h.t.Helper()
	page, err := store.New(h.pool).ListMeetings(context.WithoutCancel(h.t.Context()), h.agencyID, store.ListMeetingsParams{Limit: store.MaxMeetingsLimit})
	if err != nil {
		h.t.Fatalf("list meetings: %v", err)
	}
	stored := map[string]store.Meeting{}
	for _, m := range page.Meetings {
		stored[m.SourceID] = m
	}
	return stored
}

func TestCheckAgencyStoresMetrolinkBoardMeetingsFromRecordedFeeds(t *testing.T) {
	fake := recorded(t)
	h := newHarness(t, fake)
	h.check(recordedAt)

	stored := h.meetings()
	if len(stored) != 2 {
		t.Fatalf("stored %d meetings, want 2: %v", len(stored), stored)
	}
	want := map[string]struct {
		clip     int
		startsAt time.Time
	}{
		"board-of-directors/2026-10-23": {1436, time.Date(2026, 10, 23, 17, 15, 0, 0, time.UTC)},
		"board-of-directors/2026-11-13": {1438, time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)},
	}
	for id, w := range want {
		m := stored[id]
		if !m.StartsAt.Equal(w.startsAt) || m.Status != "held" || m.SourceURL != mediaPlayer(w.clip) || m.Title != "Board of Directors" {
			t.Errorf("meeting %s = %+v, want start %s, held, page %s", id, m, w.startsAt, mediaPlayer(w.clip))
		}
	}
	if calls := fake.Calls(); len(calls) != 2 {
		t.Errorf("requests = %d, want 2", len(calls))
	}
}

func TestUpcomingEventBecomesItsArchivedClipInOneRow(t *testing.T) {
	var feeds atomic.Pointer[[2][]byte]
	feeds.Store(&[2][]byte{rss(eventItem(612, "2026-11-13", "2026-11-06 15:57")), rss()})
	h := newHarness(t, &fetchtestFeeds{feeds: &feeds})

	h.check(time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC))
	before, ok := h.meetings()["board-of-directors/2026-11-13"]
	if !ok || before.Status != "scheduled" || !before.StartsAt.Equal(time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("upcoming meeting = %+v, want scheduled at 10:00 local", before)
	}

	feeds.Store(&[2][]byte{rss(clipItem(1438, "2026-11-13 10:15")), rss(videoItem(1438, "2026-11-13 10:15"))})
	h.check(time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC))
	stored := h.meetings()
	after := stored["board-of-directors/2026-11-13"]
	if len(stored) != 1 || after.ID != before.ID || after.Status != "held" ||
		!after.StartsAt.Equal(time.Date(2026, 11, 13, 18, 15, 0, 0, time.UTC)) || after.SourceURL != mediaPlayer(1438) {
		t.Errorf("after archiving: %+v, want the same row, held, at the clip's start", stored)
	}
}

type fetchtestFeeds struct {
	feeds *atomic.Pointer[[2][]byte]
}

func (f *fetchtestFeeds) Fetch(ctx context.Context, req fetch.Request) (fetch.Response, error) {
	current := f.feeds.Load()
	return serving(current[0], current[1]).Fetch(ctx, req)
}
