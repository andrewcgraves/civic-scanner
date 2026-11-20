//go:build integration

package rctc_test

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
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/rctc"
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

func newHarness(t *testing.T, fetcher fetch.Fetcher, followUp jobs.FollowUp) *harness {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	pool := testdb.Pool(t)
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	slug := "rctc-test-" + hex.EncodeToString(suffix)
	h := &harness{t: t, pool: pool, slug: slug, queue: "rctc_test_" + hex.EncodeToString(suffix)}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, 'live', 'https://www.rctc.org') RETURNING id`,
		slug).Scan(&h.agencyID); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM meetings WHERE agency_id = $1`, h.agencyID)
		_, _ = pool.Exec(ctx, `DELETE FROM agencies WHERE id = $1`, h.agencyID)
	})

	h.clock = sourcestest.NewClock(recordedAt)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fetcher, Clock: h.clock},
		map[string]sources.Constructor{slug: rctc.Constructor(rctc.RCTC)})
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

func TestCheckAgencyWithTheProductionConfigStoresTheCommissionMeetingAndEnqueuesItsAgenda(t *testing.T) {
	recorded := serving(fixture(t, "rctc_meetings_and_agendas.html"))
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if !strings.HasPrefix(req.URL, "https://www.rctc.org/") {
			t.Errorf("request to %s; the production config reads rctc.org only", req.URL)
		}
		return recorded.Fetch(context.Background(), req)
	}}
	h := newHarness(t, fake, nil)
	h.check(recordedAt)

	stored := h.meetings()
	m, ok := stored["commission-meeting/2026-11"]
	if len(stored) != 1 || !ok {
		t.Fatalf("stored %v, want the November commission meeting alone", stored)
	}
	if m.Status != "scheduled" || !m.StartsAt.Equal(time.Date(2026, 11, 10, 17, 30, 0, 0, time.UTC)) ||
		m.SourceURL != pageURL || m.Title != "Commission Meeting" {
		t.Errorf("meeting = %+v", m)
	}
	var agendaURL, queue string
	if err := h.pool.QueryRow(context.WithoutCancel(t.Context()),
		`SELECT args->>'agenda_url', queue FROM river_job WHERE kind = 'process_agenda' AND args->>'meeting_id' = $1`,
		m.ID.String()).Scan(&agendaURL, &queue); err != nil {
		t.Fatalf("process_agenda job: %v", err)
	}
	if agendaURL != "https://www.rctc.org/november2026agenda" || queue != h.queue {
		t.Errorf("process_agenda args agenda_url %s on queue %s", agendaURL, queue)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].URL != pageURL {
		t.Errorf("requests = %+v, want one GET of %s", calls, pageURL)
	}
}

func TestRescheduledAndCancelledMeetingUpdatesOneRow(t *testing.T) {
	var current atomic.Pointer[[]byte]
	set := func(b []byte) { current.Store(&b) }
	set(page(listed{"DECEMBER 9, 2026 &#8211; 9:30 AM", "COMMISSION MEETING"}))
	fetcher := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		return serving(*current.Load()).Fetch(context.Background(), req)
	}}
	h := newHarness(t, fetcher, jobs.NoFollowUp{})

	h.check(time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC))
	before, ok := h.meetings()["commission-meeting/2026-12"]
	if !ok || before.Status != "scheduled" {
		t.Fatalf("upcoming meeting = %+v", before)
	}

	set(page(commission("DECEMBER 16, 2026 &#8211; 1:30 PM", "december2026agenda")))
	h.check(time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC))
	set(page(listed{"DECEMBER 16, 2026 &#8211; 1:30 PM", `<a href="https://www.rctc.org/december2026agenda">COMMISSION MEETING-CANCELED</a>`}))
	h.check(time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC))

	stored := h.meetings()
	after := stored["commission-meeting/2026-12"]
	if len(stored) != 1 || after.ID != before.ID || after.Status != "cancelled" ||
		!after.StartsAt.Equal(time.Date(2026, 12, 16, 21, 30, 0, 0, time.UTC)) {
		t.Errorf("after the reschedule and cancellation: %+v, want the same row, cancelled, on 2026-12-16 13:30 local", stored)
	}
}
