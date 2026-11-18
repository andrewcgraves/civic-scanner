//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract/extracttest"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

var (
	agendaV1 = []string{
		"REGULAR BOARD MEETING",
		"Thursday, December 4, 2026, 10:00 AM",
		"1. Call to Order",
		"2. Roll Call",
		"3. Public comment on items not on the agenda; each speaker is limited to two minutes",
		"4. Approve the Consent Calendar, items 5 through 12",
		"13. Receive the quarterly ridership and budget report",
	}
	agendaV2 = append(append([]string(nil), agendaV1...), "14. Adopt the 2027 board meeting calendar")
)

func htmlAgenda(lines []string) string {
	return "<html><body><h1>" + lines[0] + "</h1><p>" + strings.Join(lines[1:], "</p><p>") + "</p></body></html>"
}

func plainAgenda(lines []string) string {
	return "  " + strings.Join(lines, "\r\n\r\n\t") + "\n\n"
}

type agendaSource struct {
	mu          sync.Mutex
	contentType string
	body        string
	errs        []error
}

func serving(contentType, body string) *agendaSource {
	return &agendaSource{contentType: contentType, body: body}
}

func (s *agendaSource) set(contentType, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentType, s.body = contentType, body
}

func (s *agendaSource) fail(errs ...error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = errs
}

func (s *agendaSource) Fetch(_ context.Context, req fetch.Request) (fetch.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return fetch.Response{}, err
	}
	return fetch.Response{Status: 200, ContentType: s.contentType, Body: []byte(s.body), FinalURL: req.URL}, nil
}

type recordingAgendaFollowUp struct {
	mu       sync.Mutex
	versions []store.AgendaVersion
	err      error
}

func (f *recordingAgendaFollowUp) AgendaVersionAdded(_ context.Context, _ pgx.Tx, v store.AgendaVersion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions = append(f.versions, v)
	return f.err
}

func (f *recordingAgendaFollowUp) got() []store.AgendaVersion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.AgendaVersion(nil), f.versions...)
}

func seedMeeting(t *testing.T, pool *pgxpool.Pool, agencyID uuid.UUID, sourceID string) store.Meeting {
	t.Helper()
	m, _, err := store.New(pool).UpsertMeeting(t.Context(), store.MeetingInput{
		AgencyID:  agencyID,
		Title:     "Board of Directors",
		StartsAt:  fixedNow.AddDate(0, 0, 7),
		Status:    "scheduled",
		SourceURL: "https://agency.test/meetings/" + sourceID,
		SourceID:  sourceID,
	})
	if err != nil {
		t.Fatalf("upsert meeting: %v", err)
	}
	return m
}

func (h *harness) processAgenda(meetingID uuid.UUID, agendaURL string, day time.Time) int64 {
	h.t.Helper()
	res, err := h.client.Insert(h.t.Context(), jobs.ProcessAgendaArgs{MeetingID: meetingID, AgendaURL: agendaURL},
		&river.InsertOpts{Queue: h.queue, ScheduledAt: day})
	if err != nil {
		h.t.Fatalf("insert process agenda job: %v", err)
	}
	if res.UniqueSkippedAsDuplicate {
		h.t.Fatalf("process agenda job on %s skipped as duplicate", day)
	}
	return res.Job.ID
}

func (h *harness) processAgendaJobs() []*rivertype.JobRow {
	h.t.Helper()
	res, err := h.client.JobList(h.t.Context(),
		river.NewJobListParams().Queues(h.queue).Kinds(jobs.ProcessAgendaArgs{}.Kind()).First(10000))
	if err != nil {
		h.t.Fatalf("list jobs: %v", err)
	}
	return res.Jobs
}

func versionsOf(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) []store.AgendaVersion {
	t.Helper()
	versions, err := store.New(pool).ListAgendaVersions(t.Context(), meetingID)
	if err != nil {
		t.Fatalf("list agenda versions: %v", err)
	}
	return versions
}

func eventTypesOf(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) []store.EventType {
	t.Helper()
	events, err := store.New(pool).ListMeetingEvents(t.Context(), meetingID)
	if err != nil {
		t.Fatalf("list meeting events: %v", err)
	}
	types := make([]store.EventType, 0, len(events))
	for _, e := range events {
		types = append(types, e.Type)
	}
	return types
}

func sameTypes(got []store.EventType, want ...store.EventType) bool {
	return slices.Equal(got, want)
}

func subprocessExtractor(t *testing.T) *extract.Extractor {
	t.Helper()
	return &extract.Extractor{PDF: extract.Subprocess{
		Binary:  testbin.BuildPackage(t, "../../cmd/extract"),
		Timeout: 20 * time.Second,
		Env:     testbin.CoverEnv(),
	}}
}

func TestCheckEnqueuesProcessAgendaAndStoresTheFirstVersionFromAPDF(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-agenda-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	withAgenda := boardMeeting("with-agenda", fixedNow.AddDate(0, 0, 7), sources.StatusScheduled)
	withAgenda.AgendaURL = "https://agency.test/with-agenda/agenda.pdf"
	withoutAgenda := boardMeeting("without-agenda", fixedNow.AddDate(0, 0, 21), sources.StatusScheduled)
	agendaFollowUp := &recordingAgendaFollowUp{}
	h := start(t, pool, jobs.Config{
		Registry:       registry(map[string]sources.Adapter{slug: sourcestest.Returning(withAgenda, withoutAgenda)}),
		Fetcher:        serving("application/octet-stream", string(extracttest.Lines(agendaV1...))),
		Extractor:      subprocessExtractor(t),
		AgendaFollowUp: agendaFollowUp,
	})

	h.checkAndWait(slug, day(0), "completed")
	meetings := meetingsOf(t, pool, agencyID)

	queued := h.processAgendaJobs()
	if len(queued) != 1 {
		t.Fatalf("process agenda jobs = %d, want 1", len(queued))
	}
	var args jobs.ProcessAgendaArgs
	if err := json.Unmarshal(queued[0].EncodedArgs, &args); err != nil {
		t.Fatal(err)
	}
	want := jobs.ProcessAgendaArgs{MeetingID: meetings["with-agenda"].ID, AgendaURL: withAgenda.AgendaURL}
	if args != want {
		t.Errorf("process agenda args = %+v, want %+v", args, want)
	}
	testdb.WaitForJobState(t, pool, queued[0].ID, "completed")

	versions := versionsOf(t, pool, want.MeetingID)
	if len(versions) != 1 {
		t.Fatalf("versions = %d, want 1", len(versions))
	}
	v := versions[0]
	wantText := strings.Join(agendaV1, "\n")
	if v.Version != 1 || v.Text != wantText || v.SourceURL != withAgenda.AgendaURL || v.ContentHash != store.HashContent(wantText) {
		t.Errorf("version = %+v", v)
	}
	if got := agendaFollowUp.got(); len(got) != 1 || got[0].ID != v.ID || got[0].Version != 1 {
		t.Errorf("agenda follow-up calls = %+v, want version 1", got)
	}
	if got := eventTypesOf(t, pool, want.MeetingID); !sameTypes(got, store.EventAgendaPublished) {
		t.Errorf("events = %v, want one agenda_published", got)
	}
	if n := len(versionsOf(t, pool, meetings["without-agenda"].ID)); n != 0 {
		t.Errorf("meeting without agenda has %d versions", n)
	}
}

func TestProcessAgendaStoresAVersionAndAnEventOnlyWhenTheTextChanges(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-agenda-versions-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "evt-versions")
	source := serving("text/html", htmlAgenda(agendaV1))
	agendaFollowUp := &recordingAgendaFollowUp{}
	h := start(t, pool, jobs.Config{Fetcher: source, AgendaFollowUp: agendaFollowUp})
	agendaURL := "https://agency.test/evt-versions/agenda"

	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(3)), "completed")
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished) {
		t.Fatalf("events after the first agenda = %v, want one agenda_published", got)
	}

	source.set("text/plain", plainAgenda(agendaV1))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(2)), "completed")
	if versions := versionsOf(t, pool, meeting.ID); len(versions) != 1 {
		t.Fatalf("versions after identical text = %d, want 1", len(versions))
	}
	if got := agendaFollowUp.got(); len(got) != 1 {
		t.Errorf("agenda follow-up calls after identical text = %d, want 1", len(got))
	}
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished) {
		t.Errorf("events after an unchanged re-run = %v, want only the first agenda_published", got)
	}

	source.set("text/html", htmlAgenda(agendaV2))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(1)), "completed")
	versions := versionsOf(t, pool, meeting.ID)
	if len(versions) != 2 || versions[0].Version != 2 || versions[0].Text != strings.Join(agendaV2, "\n") {
		t.Fatalf("versions after changed text = %+v", versions)
	}
	if got := agendaFollowUp.got(); len(got) != 2 || got[1].Version != 2 {
		t.Errorf("agenda follow-up calls = %+v, want versions 1 and 2", got)
	}
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished, store.EventAgendaChanged) {
		t.Errorf("events after a change = %v, want agenda_published then agenda_changed", got)
	}
}

func TestProcessAgendaCancelsPermanentFailuresAndRetriesTransientOnes(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-agenda-errors-"+randomSuffix(t), "live")
	gone := seedMeeting(t, pool, agencyID, "evt-gone")
	flaky := seedMeeting(t, pool, agencyID, "evt-flaky")
	source := serving("text/plain", plainAgenda(agendaV1))
	h := start(t, pool, jobs.Config{Fetcher: source})

	source.fail(&fetch.Error{StatusCode: 404, Err: errors.New("not found")})
	testdb.WaitForJobState(t, pool, h.processAgenda(gone.ID, "https://agency.test/gone.pdf", day(0)), "cancelled")
	if n := len(versionsOf(t, pool, gone.ID)); n != 0 {
		t.Errorf("a 404 stored %d versions", n)
	}

	source.fail(&fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")})
	id := h.processAgenda(flaky.ID, "https://agency.test/flaky.pdf", day(0))
	testdb.WaitForJobState(t, pool, id, "completed")
	job, err := h.client.JobGet(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Attempt != 2 || len(job.Errors) != 1 {
		t.Errorf("attempt = %d, errors = %d, want a retry after one error", job.Attempt, len(job.Errors))
	}
	if n := len(versionsOf(t, pool, flaky.ID)); n != 1 {
		t.Errorf("versions after retry = %d, want 1", n)
	}

	source.set("application/zip", "PK\x03\x04")
	testdb.WaitForJobState(t, pool, h.processAgenda(gone.ID, "https://agency.test/agenda.zip", day(0)), "cancelled")

	source.set("text/plain", "Agenda to follow.")
	testdb.WaitForJobState(t, pool, h.processAgenda(gone.ID, "https://agency.test/short.txt", day(0)), "cancelled")
	if n := len(versionsOf(t, pool, gone.ID)); n != 0 {
		t.Errorf("an implausibly short agenda stored %d versions", n)
	}

	testdb.WaitForJobState(t, pool, h.processAgenda(uuid.New(), "https://agency.test/missing.pdf", day(0)), "cancelled")
}

func TestProcessAgendaNeverStoresABotCheckPage(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-agenda-blocked-"+randomSuffix(t), "live")
	rescued := seedMeeting(t, pool, agencyID, "evt-rescued")
	stuck := seedMeeting(t, pool, agencyID, "evt-stuck")
	interstitial, err := os.ReadFile("../extract/testdata/imperva_interstitial.html")
	if err != nil {
		t.Fatal(err)
	}
	fallback := serving("text/markdown", "# "+strings.Join(agendaV1, "\n\n"))
	h := start(t, pool, jobs.Config{Fetcher: serving("text/html", string(interstitial)), Fallback: fallback})

	testdb.WaitForJobState(t, pool, h.processAgenda(rescued.ID, "https://agency.test/rescued/agenda.pdf", day(0)), "completed")
	versions := versionsOf(t, pool, rescued.ID)
	if len(versions) != 1 || !strings.Contains(versions[0].Text, "13. Receive the quarterly ridership") {
		t.Errorf("versions through the fallback = %+v", versions)
	}

	fallback.set("text/markdown", "# Pardon Our Interruption\n\nAs you were browsing something about your browser made us think you were a bot.")
	job := h.waitForError(h.processAgenda(stuck.ID, "https://agency.test/stuck/agenda.pdf", day(0)))
	if job.State == rivertype.JobStateCancelled || job.State == rivertype.JobStateDiscarded {
		t.Errorf("state = %s for a blocked agenda, want a retry", job.State)
	}
	if n := len(versionsOf(t, pool, stuck.ID)); n != 0 {
		t.Errorf("a bot check page was stored as %d versions", n)
	}
	if got := eventTypesOf(t, pool, stuck.ID); len(got) != 0 {
		t.Errorf("a bot check page recorded events %v", got)
	}
}

func TestAgendaFollowUpErrorRollsBackTheVersionAndItsEvent(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-agenda-rollback-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "evt-rollback")
	h := start(t, pool, jobs.Config{AgendaFollowUp: &recordingAgendaFollowUp{err: errors.New("summary enqueue failed")}})

	job := h.waitForError(h.processAgenda(meeting.ID, "https://agency.test/agenda.pdf", day(0)))
	if job.State == rivertype.JobStateCancelled || job.State == rivertype.JobStateDiscarded {
		t.Errorf("state = %s after a follow-up error, want a retry", job.State)
	}
	if n := len(versionsOf(t, pool, meeting.ID)); n != 0 {
		t.Errorf("versions committed despite the follow-up error: %d", n)
	}
	if got := eventTypesOf(t, pool, meeting.ID); len(got) != 0 {
		t.Errorf("events committed despite the follow-up error: %v", got)
	}
}

type failAfterEnqueue struct {
	queue  *atomic.Pointer[string]
	failOn string
}

func (f failAfterEnqueue) MeetingUpserted(ctx context.Context, tx pgx.Tx, m jobs.UpsertedMeeting) error {
	if err := (jobs.EnqueueProcessAgenda{Queue: *f.queue.Load()}).MeetingUpserted(ctx, tx, m); err != nil {
		return err
	}
	if m.Meeting.SourceID == f.failOn {
		return errors.New("later step failed")
	}
	return nil
}

func TestProcessAgendaIsEnqueuedOnlyWhenTheCheckCommits(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-agenda-tx-" + randomSuffix(t)
	seedAgency(t, pool, slug, "live")
	first := boardMeeting("a", fixedNow.AddDate(0, 0, 1), sources.StatusScheduled)
	first.AgendaURL = "https://agency.test/a/agenda.pdf"
	second := boardMeeting("b", fixedNow.AddDate(0, 0, 2), sources.StatusScheduled)
	second.AgendaURL = "https://agency.test/b/agenda.pdf"
	queue := &atomic.Pointer[string]{}
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: sourcestest.Returning(first, second)}),
		FollowUp: failAfterEnqueue{queue: queue, failOn: "b"},
	})
	queue.Store(&h.queue)

	h.waitForError(h.check(slug, day(0)))
	if queued := h.processAgendaJobs(); len(queued) != 0 {
		t.Errorf("process agenda jobs after a rolled-back check = %d, want 0", len(queued))
	}
}

type enqueueOnClosedTx struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	errs []error
}

func (f *enqueueOnClosedTx) MeetingUpserted(ctx context.Context, _ pgx.Tx, m jobs.UpsertedMeeting) error {
	closed, err := f.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := closed.Rollback(ctx); err != nil {
		return err
	}
	err = jobs.EnqueueProcessAgenda{}.MeetingUpserted(ctx, closed, m)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs = append(f.errs, err)
	return err
}

func TestEnqueueProcessAgendaReportsAnInsertError(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-agenda-insert-error-" + randomSuffix(t)
	seedAgency(t, pool, slug, "live")
	m := boardMeeting("a", fixedNow.AddDate(0, 0, 1), sources.StatusScheduled)
	m.AgendaURL = "https://agency.test/a/agenda.pdf"
	followUp := &enqueueOnClosedTx{pool: pool}
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: sourcestest.Returning(m)}),
		FollowUp: followUp,
	})

	h.waitForError(h.check(slug, day(0)))
	followUp.mu.Lock()
	defer followUp.mu.Unlock()
	if len(followUp.errs) == 0 || followUp.errs[0] == nil || !strings.Contains(followUp.errs[0].Error(), "enqueue process agenda") {
		t.Errorf("errors = %v, want an enqueue process agenda error", followUp.errs)
	}
}
