//go:build integration

package primegov_test

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
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/primegov"
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
	slug := "sbcta-test-" + hex.EncodeToString(suffix)
	h := &harness{t: t, pool: pool, slug: slug, queue: "sbcta_test_" + hex.EncodeToString(suffix)}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, 'live', 'https://www.gosbcta.com') RETURNING id`,
		slug).Scan(&h.agencyID); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM meetings WHERE agency_id = $1`, h.agencyID)
		_, _ = pool.Exec(ctx, `DELETE FROM agencies WHERE id = $1`, h.agencyID)
	})

	h.clock = sourcestest.NewClock(recordedAt)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fetcher, Clock: h.clock},
		map[string]sources.Constructor{slug: primegov.Constructor(primegov.SBCTA)})
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

func TestCheckAgencyStoresSBCTABoardMeetingsAndEnqueuesTheirAgendas(t *testing.T) {
	rec := recorded(t)
	fake := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		if !strings.HasPrefix(req.URL, "https://sbcta.primegov.com/api/v2/PublicPortal/") {
			t.Errorf("request to %s; the check reads the PrimeGov meeting lists only", req.URL)
		}
		return rec.Fetch(context.Background(), req)
	}}
	h := newHarness(t, fake, nil)
	h.check(recordedAt)

	stored := h.meetings()
	if len(stored) != 4 {
		t.Fatalf("stored %d meetings, want 100, 101, 144 and 145: %v", len(stored), stored)
	}
	m := stored["100"]
	if m.Status != "held" || !m.StartsAt.Equal(time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)) ||
		m.SourceURL != pageURL || m.Title != "Board of Directors" {
		t.Errorf("meeting 100 = %+v", m)
	}
	if s := stored["101"]; s.Status != "scheduled" {
		t.Errorf("meeting 101 = %+v, want scheduled", s)
	}
	rows, err := h.pool.Query(context.WithoutCancel(t.Context()),
		`SELECT args->>'agenda_url' FROM river_job WHERE kind = 'process_agenda' AND queue = $1`, h.queue)
	if err != nil {
		t.Fatal(err)
	}
	agendas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(agendas) != 1 || agendas[0] != packetURL+"645&compileOutputType=1" {
		t.Errorf("process_agenda jobs for %v, want one for the November packet", agendas)
	}
	if calls := fake.Calls(); len(calls) != 2 {
		t.Errorf("requests = %d, want 2", len(calls))
	}
}

func TestRenamedAndRescheduledMeetingKeepsItsRow(t *testing.T) {
	var current atomic.Pointer[[]byte]
	set := func(ms ...mtg) {
		body := list(t, ms...)
		current.Store(&body)
	}
	fetcher := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		body := []byte("[]")
		if req.URL == upcomingURL {
			body = *current.Load()
		}
		return fetch.Response{Status: 200, Body: body, FinalURL: req.URL}, nil
	}}
	h := newHarness(t, fetcher, jobs.NoFollowUp{})

	set(boardMeeting(101, "2026-12-02T10:00:00"))
	h.check(time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC))
	before, ok := h.meetings()["101"]
	if !ok || before.Status != "scheduled" {
		t.Fatalf("meeting 101 = %+v, want scheduled", before)
	}

	set(boardMeeting(101, "2026-12-03T13:30:00", packet(800, "2026-11-08T10:00:00")).titled("Special Board of Directors - CANCELLED"))
	h.check(time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC))
	stored := h.meetings()
	after := stored["101"]
	if len(stored) != 1 || after.ID != before.ID || after.Status != "cancelled" || after.Title != "Special Board of Directors" ||
		!after.StartsAt.Equal(time.Date(2026, 12, 3, 21, 30, 0, 0, time.UTC)) {
		t.Errorf("after the edit: %+v, want the same row renamed, moved and cancelled", stored)
	}
}
