//go:build integration

package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

var fixedNow = time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func seedAgency(t *testing.T, pool *pgxpool.Pool, slug, status string) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, $2, $3) RETURNING id`,
		slug, status, "https://"+slug+".example.com").Scan(&id); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM meeting_events WHERE meeting_id IN (SELECT id FROM meetings WHERE agency_id = $1)`, id)
		_, _ = pool.Exec(bg, `DELETE FROM transcripts WHERE recording_id IN (SELECT r.id FROM recordings r JOIN meetings m ON m.id = r.meeting_id WHERE m.agency_id = $1)`, id)
		_, _ = pool.Exec(bg, `DELETE FROM recordings WHERE meeting_id IN (SELECT id FROM meetings WHERE agency_id = $1)`, id)
		_, _ = pool.Exec(bg, `DELETE FROM agenda_versions WHERE meeting_id IN (SELECT id FROM meetings WHERE agency_id = $1)`, id)
		_, _ = pool.Exec(bg, `DELETE FROM meetings WHERE agency_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM agencies WHERE id = $1`, id)
	})
	return id
}

type harness struct {
	t          *testing.T
	pool       *pgxpool.Pool
	client     *river.Client[pgx.Tx]
	queue      string
	audioQueue string
}

func start(t *testing.T, pool *pgxpool.Pool, cfg jobs.Config) *harness {
	t.Helper()
	return startLogging(t, pool, cfg, slog.New(slog.DiscardHandler))
}

func startLogging(t *testing.T, pool *pgxpool.Pool, cfg jobs.Config, logger *slog.Logger) *harness {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	cfg.Queue = "jobs_test_" + randomSuffix(t)
	cfg.AudioQueue = cfg.Queue + "_audio"
	cfg.NoPeriodicJobs = true
	if cfg.Enabled == nil {
		cfg.Enabled = func(context.Context, string) bool { return true }
	}
	if cfg.Clock == nil {
		cfg.Clock = sourcestest.NewClock(fixedNow)
	}
	if cfg.Fetcher == nil {
		cfg.Fetcher = serving("text/plain", plainAgenda(agendaV1))
	}
	if cfg.Extractor == nil {
		cfg.Extractor = &extract.Extractor{PDF: extract.InProcess{}}
	}
	client, err := jobs.NewClient(pool, logger, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(ctx) })
	return &harness{t: t, pool: pool, client: client, queue: cfg.Queue, audioQueue: cfg.AudioQueue}
}

func (h *harness) check(slug string, day time.Time) int64 {
	h.t.Helper()
	res, err := h.client.Insert(h.t.Context(), jobs.CheckAgencyArgs{AgencySlug: slug},
		&river.InsertOpts{Queue: h.queue, ScheduledAt: day})
	if err != nil {
		h.t.Fatalf("insert check job: %v", err)
	}
	if res.UniqueSkippedAsDuplicate {
		h.t.Fatalf("check job for %s on %s skipped as duplicate", slug, day)
	}
	return res.Job.ID
}

func (h *harness) checkAndWait(slug string, day time.Time, state string) {
	h.t.Helper()
	testdb.WaitForJobState(h.t, h.pool, h.check(slug, day), state)
}

func (h *harness) waitForError(id int64) *rivertype.JobRow {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		job, err := h.client.JobGet(h.t.Context(), id)
		if err != nil {
			h.t.Fatalf("get job %d: %v", id, err)
		}
		if len(job.Errors) > 0 {
			return job
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("job %d recorded no error after 15s", id)
	return nil
}

func (h *harness) enqueueChecks(slot time.Time) bool {
	h.t.Helper()
	res, err := h.client.Insert(h.t.Context(), jobs.EnqueueAgencyChecksArgs{Slot: slot}, &river.InsertOpts{Queue: h.queue})
	if err != nil {
		h.t.Fatalf("insert enqueue job: %v", err)
	}
	if res.UniqueSkippedAsDuplicate {
		return false
	}
	testdb.WaitForJobState(h.t, h.pool, res.Job.ID, "completed")
	return true
}

func (h *harness) checkJobsBySlug() map[string]int {
	h.t.Helper()
	res, err := h.client.JobList(h.t.Context(),
		river.NewJobListParams().Queues(h.queue).Kinds(jobs.CheckAgencyArgs{}.Kind()).First(10000))
	if err != nil {
		h.t.Fatalf("list jobs: %v", err)
	}
	counts := map[string]int{}
	for _, j := range res.Jobs {
		var args jobs.CheckAgencyArgs
		if err := json.Unmarshal(j.EncodedArgs, &args); err != nil {
			h.t.Fatal(err)
		}
		counts[args.AgencySlug]++
	}
	return counts
}

func meetingsOf(t *testing.T, pool *pgxpool.Pool, agencyID uuid.UUID) map[string]store.Meeting {
	t.Helper()
	page, err := store.New(pool).ListMeetings(t.Context(), agencyID, store.ListMeetingsParams{Limit: store.MaxMeetingsLimit})
	if err != nil {
		t.Fatalf("list meetings: %v", err)
	}
	bySource := map[string]store.Meeting{}
	for _, m := range page.Meetings {
		bySource[m.SourceID] = m
	}
	return bySource
}

func day(n int) time.Time {
	return time.Now().UTC().AddDate(0, 0, -n)
}

func boardMeeting(id string, startsAt time.Time, status sources.Status) sources.Meeting {
	return sources.Meeting{
		SourceID: id,
		Title:    "Board of Directors " + id,
		StartsAt: startsAt,
		Status:   status,
		PageURL:  "https://agency.test/meetings/" + id,
	}
}
