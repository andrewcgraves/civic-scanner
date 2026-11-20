//go:build integration

package jobs_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func (h *harness) manualCheck(slugs ...string) []jobs.ManualCheck {
	h.t.Helper()
	checks, err := jobs.EnqueueManualChecks(h.t.Context(), h.client, h.queue, slugs)
	if err != nil {
		h.t.Fatalf("enqueue manual checks: %v", err)
	}
	return checks
}

func (h *harness) agendaJobsByTrigger() (scheduled, manual []*rivertype.JobRow) {
	h.t.Helper()
	for _, j := range h.processAgendaJobs() {
		var args jobs.ProcessAgendaArgs
		if err := json.Unmarshal(j.EncodedArgs, &args); err != nil {
			h.t.Fatal(err)
		}
		if args.Manual {
			manual = append(manual, j)
		} else {
			scheduled = append(scheduled, j)
		}
	}
	return scheduled, manual
}

func (h *harness) checkJobsOf(slug string) (scheduled, manual int) {
	h.t.Helper()
	res, err := h.client.JobList(h.t.Context(),
		river.NewJobListParams().Queues(h.queue).Kinds(jobs.CheckAgencyArgs{}.Kind()).First(10000))
	if err != nil {
		h.t.Fatalf("list jobs: %v", err)
	}
	for _, j := range res.Jobs {
		var args jobs.CheckAgencyArgs
		if err := json.Unmarshal(j.EncodedArgs, &args); err != nil {
			h.t.Fatal(err)
		}
		switch {
		case args.AgencySlug != slug:
		case args.Manual:
			manual++
		default:
			scheduled++
		}
	}
	return scheduled, manual
}

func eventCount(t *testing.T, pool *pgxpool.Pool, agencyID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM meeting_events WHERE meeting_id IN (SELECT id FROM meetings WHERE agency_id = $1)`, agencyID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func insertOnlyClient(t *testing.T, pool *pgxpool.Pool) *river.Client[pgx.Tx] {
	t.Helper()
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func finish(t *testing.T, pool *pgxpool.Pool, jobID int64, state rivertype.JobState) {
	t.Helper()
	if _, err := pool.Exec(t.Context(),
		`UPDATE river_job SET state = $2, finalized_at = now(), attempt = 1, attempted_at = now() WHERE id = $1`, jobID, string(state)); err != nil {
		t.Fatalf("finish job %d as %s: %v", jobID, state, err)
	}
}

func TestManualCheckReprocessesAgendasAfterASameDayScheduledCheck(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-manual-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	upcoming := boardMeeting("upcoming", fixedNow.AddDate(0, 0, 3), sources.StatusScheduled)
	upcoming.AgendaURL = "https://agency.test/upcoming/agenda.txt"
	held := heldWithRecording("held")
	fake := sourcestest.Returning(upcoming, held)
	downloader := &fakeDownloader{}
	agendaFollowUp := &recordingAgendaFollowUp{}
	h := start(t, pool, jobs.Config{
		Registry:       registry(map[string]sources.Adapter{slug: fake}),
		Storage:        storagetest.New(),
		Audio:          downloader,
		AudioFollowUp:  &recordingAudioFollowUp{},
		AgendaFollowUp: agendaFollowUp,
	})

	h.checkAndWait(slug, day(0), "completed")
	scheduledAgenda, _ := h.agendaJobsByTrigger()
	if len(scheduledAgenda) != 1 {
		t.Fatalf("process agenda jobs after the scheduled check = %d, want 1", len(scheduledAgenda))
	}
	testdb.WaitForJobState(t, pool, scheduledAgenda[0].ID, "completed")
	downloads := h.downloadJobs()
	if len(downloads) != 1 {
		t.Fatalf("download jobs after the scheduled check = %d, want 1", len(downloads))
	}
	testdb.WaitForJobState(t, pool, downloads[0].ID, "completed")
	meeting := meetingsOf(t, pool, agencyID)["upcoming"]
	if n := len(versionsOf(t, pool, meeting.ID)); n != 1 {
		t.Fatalf("agenda versions after the scheduled check = %d, want 1", n)
	}
	events := eventCount(t, pool, agencyID)

	again, err := h.client.Insert(t.Context(), jobs.CheckAgencyArgs{AgencySlug: slug}, &river.InsertOpts{Queue: h.queue, ScheduledAt: day(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !again.UniqueSkippedAsDuplicate {
		t.Fatal("a second scheduled check the same day was not skipped; the test no longer shows what the manual check bypasses")
	}

	checks := h.manualCheck(slug)
	if len(checks) != 1 || checks[0].Duplicate || checks[0].AgencySlug != slug {
		t.Fatalf("manual checks = %+v, want one new check for %s", checks, slug)
	}
	testdb.WaitForJobState(t, pool, checks[0].JobID, "completed")
	if calls := fake.Calls(); len(calls) != 2 {
		t.Errorf("adapter calls = %d, want 2 (scheduled and manual)", len(calls))
	}
	_, manualAgenda := h.agendaJobsByTrigger()
	if len(manualAgenda) != 1 {
		t.Fatalf("manual process agenda jobs = %d, want 1 despite the completed job the same day", len(manualAgenda))
	}
	testdb.WaitForJobState(t, pool, manualAgenda[0].ID, "completed")

	if n := len(versionsOf(t, pool, meeting.ID)); n != 1 {
		t.Errorf("agenda versions after re-processing an unchanged agenda = %d, want 1", n)
	}
	if got := eventCount(t, pool, agencyID); got != events {
		t.Errorf("meeting events after the manual check = %d, want %d", got, events)
	}
	if got := agendaFollowUp.got(); len(got) != 1 {
		t.Errorf("agenda follow-ups (summaries) = %d, want 1", len(got))
	}
	if got := h.downloadJobs(); len(got) != 1 {
		t.Errorf("download jobs after the manual check = %d, want still 1", len(got))
	}
	if calls := downloader.calls(); len(calls) != 1 {
		t.Errorf("downloads after the manual check = %d, want 1", len(calls))
	}

	tomorrow := day(-1)
	h.check(slug, tomorrow)
	h.processAgenda(meeting.ID, upcoming.AgendaURL, tomorrow)
}

func TestManualCheckReprocessesAnAgendaWhoseJobWasCancelledTheSameDay(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-manual-cancelled-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	upcoming := boardMeeting("upcoming", fixedNow.AddDate(0, 0, 3), sources.StatusScheduled)
	upcoming.AgendaURL = "https://agency.test/upcoming/agenda.txt"
	source := serving("text/plain", plainAgenda(agendaV1))
	source.fail(&fetch.Error{StatusCode: 404, Err: errors.New("not found")})
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: sourcestest.Returning(upcoming)}),
		Fetcher:  source,
	})

	h.checkAndWait(slug, day(0), "completed")
	scheduledAgenda, _ := h.agendaJobsByTrigger()
	if len(scheduledAgenda) != 1 {
		t.Fatalf("process agenda jobs = %d, want 1", len(scheduledAgenda))
	}
	testdb.WaitForJobState(t, pool, scheduledAgenda[0].ID, "cancelled")
	meeting := meetingsOf(t, pool, agencyID)["upcoming"]
	if n := len(versionsOf(t, pool, meeting.ID)); n != 0 {
		t.Fatalf("agenda versions after the cancelled job = %d, want 0", n)
	}

	testdb.WaitForJobState(t, pool, h.manualCheck(slug)[0].JobID, "completed")
	_, manualAgenda := h.agendaJobsByTrigger()
	if len(manualAgenda) != 1 {
		t.Fatalf("manual process agenda jobs = %d, want 1", len(manualAgenda))
	}
	testdb.WaitForJobState(t, pool, manualAgenda[0].ID, "completed")
	versions := versionsOf(t, pool, meeting.ID)
	if len(versions) != 1 {
		t.Fatalf("agenda versions after the manual check = %d, want 1", len(versions))
	}
	if events := eventTypesOf(t, pool, meeting.ID); len(events) != 1 {
		t.Errorf("events = %v, want one for the first published agenda", events)
	}
}

func TestRepeatedManualChecksDoNotStackWhileOneIsWaiting(t *testing.T) {
	pool := testdb.Pool(t)
	client := insertOnlyClient(t, pool)
	queue := "jobs_test_unworked_" + randomSuffix(t)
	slug := "jobs-test-repeat-" + randomSuffix(t)
	other := "jobs-test-repeat-other-" + randomSuffix(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM river_job WHERE queue = $1`, queue)
	})
	enqueue := func(slugs ...string) []jobs.ManualCheck {
		t.Helper()
		checks, err := jobs.EnqueueManualChecks(t.Context(), client, queue, slugs)
		if err != nil {
			t.Fatal(err)
		}
		return checks
	}

	first := enqueue(slug)
	if len(first) != 1 || first[0].Duplicate {
		t.Fatalf("first manual check = %+v", first)
	}
	second := enqueue(slug, other)
	bySlug := map[string]jobs.ManualCheck{}
	for _, c := range second {
		bySlug[c.AgencySlug] = c
	}
	if c := bySlug[slug]; !c.Duplicate || c.JobID != first[0].JobID || c.State != rivertype.JobStateAvailable {
		t.Errorf("second manual check for %s = %+v, want the waiting job %d reported as a duplicate", slug, c, first[0].JobID)
	}
	if c := bySlug[other]; c.Duplicate || c.JobID == 0 {
		t.Errorf("manual check for %s = %+v, want a new job", other, c)
	}

	for _, state := range []rivertype.JobState{rivertype.JobStateRunning, rivertype.JobStateRetryable} {
		if _, err := pool.Exec(t.Context(), `UPDATE river_job SET state = $2, attempt = 1, attempted_at = now() WHERE id = $1`, first[0].JobID, string(state)); err != nil {
			t.Fatal(err)
		}
		if c := enqueue(slug)[0]; !c.Duplicate || c.JobID != first[0].JobID {
			t.Errorf("manual check while one is %s = %+v, want a duplicate", state, c)
		}
	}

	previous := first[0].JobID
	for _, state := range []rivertype.JobState{rivertype.JobStateCompleted, rivertype.JobStateCancelled, rivertype.JobStateDiscarded} {
		finish(t, pool, previous, state)
		c := enqueue(slug)[0]
		if c.Duplicate || c.JobID == previous {
			t.Errorf("manual check after the last one was %s = %+v, want a new job", state, c)
		}
		previous = c.JobID
	}
}

func TestManualCheckLeavesTheScheduledChecksOfThatDayAndTheNextAlone(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-manual-schedule-" + randomSuffix(t)
	seedAgency(t, pool, slug, "live")
	fake := sourcestest.Returning(boardMeeting("m-1", fixedNow.AddDate(0, 0, 3), sources.StatusScheduled))
	h := start(t, pool, jobs.Config{Registry: registry(map[string]sources.Adapter{slug: fake})})

	today := time.Now().UTC().Truncate(24 * time.Hour)
	testdb.WaitForJobState(t, pool, h.manualCheck(slug)[0].JobID, "completed")

	if !h.enqueueChecks(today) {
		t.Fatal("the daily enqueue for today was skipped after a manual check")
	}
	if scheduled, manual := h.checkJobsOf(slug); scheduled != 1 || manual != 1 {
		t.Fatalf("check jobs after today's daily enqueue = %d scheduled, %d manual; want 1 and 1", scheduled, manual)
	}
	if !h.enqueueChecks(today.Add(time.Hour)) {
		t.Fatal("a second daily enqueue for today was skipped as a duplicate slot")
	}
	if scheduled, _ := h.checkJobsOf(slug); scheduled != 1 {
		t.Errorf("scheduled checks after a second enqueue today = %d, want 1: the manual check must not make a duplicate", scheduled)
	}

	testdb.WaitForJobState(t, pool, h.manualCheck(slug)[0].JobID, "completed")
	if !h.enqueueChecks(today.AddDate(0, 0, 1)) {
		t.Fatal("tomorrow's daily enqueue was skipped")
	}
	if scheduled, manual := h.checkJobsOf(slug); scheduled != 2 || manual != 2 {
		t.Errorf("check jobs after tomorrow's enqueue = %d scheduled, %d manual; want 2 and 2", scheduled, manual)
	}
}
