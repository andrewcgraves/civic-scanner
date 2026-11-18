//go:build integration

package vctc_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/vctc"
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

func newHarness(t *testing.T, cfg vctc.Config, fetcher fetch.Fetcher, followUp jobs.FollowUp) *harness {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	pool := testdb.Pool(t)
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	slug := "vctc-test-" + hex.EncodeToString(suffix)
	h := &harness{t: t, pool: pool, slug: slug, queue: "vctc_test_" + hex.EncodeToString(suffix)}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, 'live', 'https://www.goventura.org') RETURNING id`,
		slug).Scan(&h.agencyID); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM meetings WHERE agency_id = $1`, h.agencyID)
		_, _ = pool.Exec(ctx, `DELETE FROM agencies WHERE id = $1`, h.agencyID)
	})

	h.clock = sourcestest.NewClock(recordedAt)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fetcher, Clock: h.clock},
		map[string]sources.Constructor{slug: vctc.Constructor(cfg)})
	client, err := jobs.NewClient(pool, slog.New(slog.DiscardHandler), jobs.Config{
		Registry:       registry,
		Clock:          h.clock,
		Enabled:        func(context.Context, string) bool { return true },
		Queue:          h.queue,
		NoPeriodicJobs: true,
		FollowUp:       followUp,
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

func TestCheckAgencyStoresVCTCCommissionMeetingsFromRecordedResponses(t *testing.T) {
	fake := serving(fixture(t, "vctc_commission_meetings.json"), fixture(t, "vctc_youtube_feed.xml"))
	h := newHarness(t, withYouTube(), fake, jobs.NoFollowUp{})
	h.check(recordedAt)

	stored := h.meetings()
	m, ok := stored["24950"]
	if len(stored) != 1 || !ok {
		t.Fatalf("stored %v, want meeting 24950 alone", stored)
	}
	if m.Status != "held" || !m.StartsAt.Equal(time.Date(2026, 11, 6, 17, 0, 0, 0, time.UTC)) ||
		m.SourceURL != "https://www.goventura.org/meeting/commission-meeting-november-2026/" || m.Title != "Commission Meeting – November 2026" {
		t.Errorf("meeting 24950 = %+v", m)
	}
	if calls := fake.Calls(); len(calls) != 2 {
		t.Errorf("requests = %d, want 2", len(calls))
	}
}

func TestUpcomingPostBecomesHeldInOneRow(t *testing.T) {
	type state struct{ posts, feed []byte }
	var current atomic.Pointer[state]
	current.Store(&state{posts(t, commission(24950, "November 2026", "2026-11-06", "09:00", uploads+"2026/10/AGENDA-COMMISSION-11.6.26.pdf")), feed()})
	fetcher := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		s := current.Load()
		return serving(s.posts, s.feed).Fetch(context.Background(), req)
	}}
	h := newHarness(t, withYouTube(), fetcher, jobs.NoFollowUp{})

	h.check(time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC))
	before, ok := h.meetings()["24950"]
	if !ok || before.Status != "scheduled" || !before.StartsAt.Equal(time.Date(2026, 11, 6, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("upcoming meeting = %+v, want scheduled at 09:00 local", before)
	}

	current.Store(&state{
		posts(t, commission(24950, "November 2026", "2026-11-06", "09:00", uploads+"2026/11/AMENDED-AGENDA-COMMISSION-11.6.26.pdf")),
		feed(videoEntry{"3WbJq9XWnNs", "VCTC Commission Meeting - November 11, 2026", "2026-11-07T06:42:53+00:00"}),
	})
	h.check(time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC))
	stored := h.meetings()
	after := stored["24950"]
	if len(stored) != 1 || after.ID != before.ID || after.Status != "held" {
		t.Errorf("after the meeting: %+v, want the same row, held", stored)
	}
}

func TestCheckAgencyWithTheProductionConfigStoresMeetingsAndEnqueuesAgendas(t *testing.T) {
	recorded := serving(fixture(t, "vctc_commission_meetings.json"), nil)
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if !strings.HasPrefix(req.URL, "https://www.goventura.org/") {
			t.Errorf("request to %s; the production config reads goventura.org only", req.URL)
		}
		return recorded.Fetch(context.Background(), req)
	}}
	h := newHarness(t, vctc.VCTC, fake, nil)
	h.check(recordedAt)

	stored := h.meetings()
	m, ok := stored["24950"]
	if len(stored) != 1 || !ok {
		t.Fatalf("stored %v, want meeting 24950 alone", stored)
	}
	if m.Status != "held" || !m.StartsAt.Equal(time.Date(2026, 11, 6, 17, 0, 0, 0, time.UTC)) ||
		m.SourceURL != "https://www.goventura.org/meeting/commission-meeting-november-2026/" || m.Title != "Commission Meeting – November 2026" {
		t.Errorf("meeting 24950 = %+v, want held on its presentations, at 09:00 local", m)
	}
	var agendaURL, queue string
	if err := h.pool.QueryRow(context.WithoutCancel(t.Context()),
		`SELECT args->>'agenda_url', queue FROM river_job WHERE kind = 'process_agenda' AND args->>'meeting_id' = $1`,
		m.ID.String()).Scan(&agendaURL, &queue); err != nil {
		t.Fatalf("process_agenda job for meeting 24950: %v", err)
	}
	if agendaURL != uploads+"2026/10/AGENDA-COMMISSION-11.6.26.pdf" || queue != h.queue {
		t.Errorf("process_agenda args agenda_url %s on queue %s", agendaURL, queue)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].URL != postsURL {
		t.Errorf("requests = %+v, want one GET of %s", calls, postsURL)
	}
}
