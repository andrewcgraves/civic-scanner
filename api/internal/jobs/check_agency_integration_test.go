//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func registry(adapters map[string]sources.Adapter) *sources.Registry {
	constructors := map[string]sources.Constructor{}
	for slug, a := range adapters {
		constructors[slug] = sourcestest.Constructor(a)
	}
	return sources.NewRegistry(sources.Deps{}, constructors)
}

func TestCheckAgencyJobWithDefaultConfigCompletes(t *testing.T) {
	ctx := context.WithoutCancel(t.Context())
	pool := testdb.Pool(t)
	queue := "jobs_test_" + randomSuffix(t)

	client, err := jobs.NewClient(pool, slog.New(slog.DiscardHandler), jobs.Config{Queue: queue})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(ctx) })

	inserted, err := client.Insert(ctx, jobs.CheckAgencyArgs{AgencySlug: "jobs-test-default-" + randomSuffix(t)}, &river.InsertOpts{Queue: queue})
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	testdb.WaitForJobState(t, pool, inserted.Job.ID, "completed")
}

func TestEnqueueChecksOnePerLiveAgencyAndNoneForPending(t *testing.T) {
	pool := testdb.Pool(t)
	suffix := randomSuffix(t)
	live := "jobs-test-live-" + suffix
	pending := "jobs-test-pending-" + suffix
	seedAgency(t, pool, live, "live")
	seedAgency(t, pool, pending, "pending")
	h := start(t, pool, jobs.Config{})

	slot := time.Now().UTC().Truncate(24 * time.Hour).Add(time.Hour)
	if !h.enqueueChecks(slot) {
		t.Fatal("first enqueue skipped as duplicate")
	}

	counts := h.checkJobsBySlug()
	for _, slug := range []string{live, "la-metro", "metrolink", "octa", "rctc", "sbcta", "vctc"} {
		if counts[slug] != 1 {
			t.Errorf("check jobs for %s = %d, want 1", slug, counts[slug])
		}
	}
	if counts[pending] != 0 {
		t.Errorf("check jobs for pending agency = %d, want 0", counts[pending])
	}

	if h.enqueueChecks(slot) {
		t.Error("second enqueue for the same slot was not skipped")
	}
	if !h.enqueueChecks(slot.Add(time.Hour)) {
		t.Fatal("enqueue for a later slot on the same day was skipped")
	}
	for slug, n := range h.checkJobsBySlug() {
		if n != 1 {
			t.Errorf("after a second run the same day: check jobs for %s = %d, want 1", slug, n)
		}
	}
}

func TestFlagOffEnqueuesNothingAndSkipsTheAdapter(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-flag-off-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	fake := sourcestest.Returning(boardMeeting("m-1", fixedNow, sources.StatusScheduled))
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: fake}),
		Enabled:  func(context.Context, string) bool { return false },
	})

	h.enqueueChecks(time.Now().UTC())
	if counts := h.checkJobsBySlug(); len(counts) != 0 {
		t.Errorf("check jobs enqueued with the flag off: %v", counts)
	}

	h.checkAndWait(slug, day(0), "completed")
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("adapter called %d times with the flag off", len(calls))
	}
	if got := meetingsOf(t, pool, agencyID); len(got) != 0 {
		t.Errorf("meetings written with the flag off: %v", got)
	}
}

func TestCheckUpsertsMeetingsWithoutDuplicatesAndAppliesChanges(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-upsert-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	upcoming := boardMeeting("evt-1", fixedNow.AddDate(0, 0, 3), sources.StatusScheduled)
	upcoming.AgendaURL = "https://agency.test/evt-1/agenda.pdf"
	past := boardMeeting("evt-2", fixedNow.AddDate(0, 0, -10), sources.StatusHeld)
	past.RecordingURL = "https://agency.test/evt-2/video.mp4"
	fake := sourcestest.Returning(upcoming, past)
	h := start(t, pool, jobs.Config{Registry: registry(map[string]sources.Adapter{slug: fake})})

	h.checkAndWait(slug, day(3), "completed")
	first := meetingsOf(t, pool, agencyID)
	if len(first) != 2 {
		t.Fatalf("meetings after first run = %d, want 2", len(first))
	}
	got := first["evt-1"]
	if got.Title != upcoming.Title || !got.StartsAt.Equal(upcoming.StartsAt) || got.Status != "scheduled" || got.SourceURL != upcoming.PageURL {
		t.Errorf("evt-1 = %+v", got)
	}
	if first["evt-2"].Status != "held" {
		t.Errorf("evt-2 status = %q, want held", first["evt-2"].Status)
	}
	wantWindow := sources.Window{From: fixedNow.AddDate(0, 0, -30), To: fixedNow.AddDate(0, 0, 90)}
	if calls := fake.Calls(); len(calls) != 1 || calls[0] != wantWindow {
		t.Errorf("adapter windows = %v, want [%v]", calls, wantWindow)
	}

	h.checkAndWait(slug, day(2), "completed")
	second := meetingsOf(t, pool, agencyID)
	if len(second) != 2 || second["evt-1"].ID != first["evt-1"].ID || second["evt-2"].ID != first["evt-2"].ID {
		t.Errorf("second run changed the meeting set: %v", second)
	}

	upcoming.Title = "Special Board Meeting"
	upcoming.StartsAt = upcoming.StartsAt.Add(90 * time.Minute)
	fake.Set(upcoming, past)
	h.checkAndWait(slug, day(1), "completed")
	changed := meetingsOf(t, pool, agencyID)["evt-1"]
	if changed.ID != first["evt-1"].ID || changed.Title != "Special Board Meeting" || !changed.StartsAt.Equal(upcoming.StartsAt) {
		t.Errorf("evt-1 after change = %+v", changed)
	}
}

func TestCheckNeverMovesAHeldMeetingBackToScheduled(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-held-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	m := boardMeeting("evt-held", fixedNow.AddDate(0, 0, -2), sources.StatusHeld)
	fake := sourcestest.Returning(m)
	h := start(t, pool, jobs.Config{Registry: registry(map[string]sources.Adapter{slug: fake})})

	h.checkAndWait(slug, day(2), "completed")
	m.Status = sources.StatusScheduled
	fake.Set(m)
	h.checkAndWait(slug, day(1), "completed")

	if got := meetingsOf(t, pool, agencyID)["evt-held"].Status; got != "held" {
		t.Errorf("status = %q, want held", got)
	}
}

func TestCheckSkipsAgenciesWithoutAdapterOrNotLive(t *testing.T) {
	pool := testdb.Pool(t)
	suffix := randomSuffix(t)
	unadapted := "jobs-test-no-adapter-" + suffix
	pending := "jobs-test-not-live-" + suffix
	unadaptedID := seedAgency(t, pool, unadapted, "live")
	pendingID := seedAgency(t, pool, pending, "pending")
	fake := sourcestest.Returning(boardMeeting("m-1", fixedNow, sources.StatusScheduled))
	h := start(t, pool, jobs.Config{Registry: registry(map[string]sources.Adapter{pending: fake})})

	h.checkAndWait(unadapted, day(0), "completed")
	h.checkAndWait(pending, day(0), "completed")

	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("adapter of a pending agency called %d times", len(calls))
	}
	if n := len(meetingsOf(t, pool, unadaptedID)) + len(meetingsOf(t, pool, pendingID)); n != 0 {
		t.Errorf("meetings written for skipped agencies: %d", n)
	}
}

func TestAdapterErrorOnOneAgencyDoesNotAffectAnother(t *testing.T) {
	pool := testdb.Pool(t)
	suffix := randomSuffix(t)
	broken := "jobs-test-broken-" + suffix
	healthy := "jobs-test-healthy-" + suffix
	invalid := "jobs-test-invalid-" + suffix
	seedAgency(t, pool, broken, "live")
	healthyID := seedAgency(t, pool, healthy, "live")
	invalidID := seedAgency(t, pool, invalid, "live")
	h := start(t, pool, jobs.Config{Registry: registry(map[string]sources.Adapter{
		broken:  sourcestest.Failing(&fetch.Error{StatusCode: 404, Err: errors.New("calendar moved")}),
		healthy: sourcestest.Returning(boardMeeting("ok", fixedNow, sources.StatusScheduled)),
		invalid: sourcestest.Returning(boardMeeting("ok", fixedNow, sources.StatusScheduled), boardMeeting("", fixedNow, sources.StatusScheduled)),
	})})

	brokenJob := h.check(broken, day(0))
	healthyJob := h.check(healthy, day(0))
	invalidJob := h.check(invalid, day(0))
	testdb.WaitForJobState(t, pool, brokenJob, "cancelled")
	testdb.WaitForJobState(t, pool, invalidJob, "cancelled")
	testdb.WaitForJobState(t, pool, healthyJob, "completed")

	if got := meetingsOf(t, pool, healthyID); len(got) != 1 {
		t.Errorf("healthy agency meetings = %d, want 1", len(got))
	}
	if got := meetingsOf(t, pool, invalidID); len(got) != 0 {
		t.Errorf("an invalid result wrote %d meetings, want none", len(got))
	}
}

type flakyAdapter struct {
	mu       sync.Mutex
	attempts int
}

func (a *flakyAdapter) Meetings(context.Context, sources.Window) ([]sources.Meeting, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attempts++
	if a.attempts == 1 {
		return nil, &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}
	}
	return []sources.Meeting{boardMeeting("after-retry", fixedNow, sources.StatusScheduled)}, nil
}

func TestRetryableAdapterErrorIsRetried(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-flaky-" + randomSuffix(t)
	seedAgency(t, pool, slug, "live")
	h := start(t, pool, jobs.Config{Registry: registry(map[string]sources.Adapter{slug: &flakyAdapter{}})})

	id := h.check(slug, day(0))
	testdb.WaitForJobState(t, pool, id, "completed")
	job, err := h.client.JobGet(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Attempt != 2 || len(job.Errors) != 1 {
		t.Errorf("attempt = %d, errors = %d, want a retry after one error", job.Attempt, len(job.Errors))
	}
}

type recordingFollowUp struct {
	mu      sync.Mutex
	got     []jobs.UpsertedMeeting
	visible []bool
	err     error
	pool    *pgxpool.Pool
}

func (f *recordingFollowUp) MeetingUpserted(ctx context.Context, tx pgx.Tx, m jobs.UpsertedMeeting) error {
	var inTx, outside int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM meetings WHERE id = $1`, m.Meeting.ID).Scan(&inTx); err != nil {
		return err
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM meetings WHERE id = $1`, m.Meeting.ID).Scan(&outside); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, m)
	f.visible = append(f.visible, inTx == 1 && outside == 0)
	return f.err
}

func TestFollowUpSeesEachMeetingInsideTheUpsertTransaction(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-follow-up-" + randomSuffix(t)
	seedAgency(t, pool, slug, "live")
	withAgenda := boardMeeting("agenda", fixedNow.AddDate(0, 0, 7), sources.StatusScheduled)
	withAgenda.AgendaURL = "https://agency.test/agenda.pdf"
	withRecording := boardMeeting("recording", fixedNow.AddDate(0, 0, -7), sources.StatusHeld)
	withRecording.RecordingURL = "https://agency.test/video.mp4"
	followUp := &recordingFollowUp{pool: pool}
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: sourcestest.Returning(withAgenda, withRecording)}),
		FollowUp: followUp,
	})

	h.checkAndWait(slug, day(0), "completed")

	followUp.mu.Lock()
	defer followUp.mu.Unlock()
	if len(followUp.got) != 2 {
		t.Fatalf("follow-up calls = %d, want 2", len(followUp.got))
	}
	bySource := map[string]jobs.UpsertedMeeting{}
	for i, m := range followUp.got {
		bySource[m.Meeting.SourceID] = m
		if !followUp.visible[i] {
			t.Errorf("follow-up for %s ran outside the upsert transaction", m.Meeting.SourceID)
		}
		if !m.Created {
			t.Errorf("follow-up for %s: Created = false on first sight", m.Meeting.SourceID)
		}
	}
	if a := bySource["agenda"]; a.AgendaURL != withAgenda.AgendaURL || a.RecordingURL != "" {
		t.Errorf("agenda follow-up = %+v", a)
	}
	if r := bySource["recording"]; r.RecordingURL != withRecording.RecordingURL || r.AgendaURL != "" {
		t.Errorf("recording follow-up = %+v", r)
	}
}

func TestFollowUpErrorRollsBackEveryUpsert(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-follow-up-fail-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	followUp := &recordingFollowUp{pool: pool, err: errors.New("enqueue failed")}
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: sourcestest.Returning(
			boardMeeting("a", fixedNow, sources.StatusScheduled),
			boardMeeting("b", fixedNow.AddDate(0, 0, 1), sources.StatusScheduled),
		)}),
		FollowUp: followUp,
	})

	job := h.waitForError(h.check(slug, day(0)))
	if job.State == rivertype.JobStateCancelled || job.State == rivertype.JobStateDiscarded {
		t.Errorf("state = %s after a follow-up error, want a retry", job.State)
	}
	if got := meetingsOf(t, pool, agencyID); len(got) != 0 {
		t.Errorf("meetings committed despite the follow-up error: %d", len(got))
	}
}
