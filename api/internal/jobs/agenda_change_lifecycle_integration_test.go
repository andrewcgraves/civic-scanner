//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type switchableFlag struct {
	mu  sync.Mutex
	off bool
}

func (f *switchableFlag) set(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.off = !on
}

func (f *switchableFlag) enabled(_ context.Context, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return name != jobs.FlagAgendaChanges || !f.off
}

type countingModel struct {
	changes   atomic.Int32
	summaries atomic.Int32
}

func (m *countingModel) fake() *llmtest.Fake {
	return &llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		if isChangeRequest(req) {
			m.changes.Add(1)
			return llm.Response{Text: "The model describes the change.", FinishReason: "stop"}, nil
		}
		m.summaries.Add(1)
		return llm.Response{Text: "Summary.", FinishReason: "stop"}, nil
	}}
}

func addDeferred(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID, lines []string) store.AgendaVersion {
	t.Helper()
	text := strings.Join(lines, "\n")
	v, _, err := store.New(pool).AddAgendaVersion(t.Context(), store.AgendaVersionInput{
		MeetingID: meetingID, SourceURL: "https://agency.test/agenda.pdf", Text: text,
		ContentHash: store.HashContent(text), DeferChangeEvent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (h *harness) describe(id uuid.UUID, opts *river.InsertOpts) int64 {
	h.t.Helper()
	if opts == nil {
		opts = &river.InsertOpts{}
	}
	opts.Queue = h.queue
	res, err := h.client.Insert(h.t.Context(), jobs.DescribeAgendaChangeArgs{AgendaVersionID: id}, opts)
	if err != nil || res.UniqueSkippedAsDuplicate {
		h.t.Fatalf("insert describe = (%+v, %v)", res, err)
	}
	return res.Job.ID
}

func (h *harness) describeJobOf(id uuid.UUID) int64 {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range h.describeJobs() {
			if strings.Contains(string(j.EncodedArgs), id.String()) {
				return j.ID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("no describe job for %s", id)
	return 0
}

func (h *harness) summarizeJobOf(id uuid.UUID) int64 {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := h.client.JobList(h.t.Context(), river.NewJobListParams().Queues(h.queue).Kinds(jobs.SummarizeAgendaArgs{}.Kind()).First(100))
		if err != nil {
			h.t.Fatal(err)
		}
		for _, j := range res.Jobs {
			if strings.Contains(string(j.EncodedArgs), id.String()) {
				return j.ID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("no summarize job for %s", id)
	return 0
}

func wake(t *testing.T, pool *pgxpool.Pool, jobID int64) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE river_job SET scheduled_at = now() WHERE id = $1 AND state = 'scheduled'`, jobID); err != nil {
		t.Fatal(err)
	}
}

func changePayloads(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) []store.AgendaEventPayload {
	t.Helper()
	var payloads []store.AgendaEventPayload
	for _, e := range agendaEvents(t, pool, meetingID) {
		if e.Type != store.EventAgendaChanged {
			continue
		}
		var p store.AgendaEventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, p)
	}
	return payloads
}

func failChangeEvents(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID, once bool) func() {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	name := "jobs_test_fail_" + randomSuffix(t)
	condition := "true"
	if once {
		condition = "nextval('" + name + "_seq') = 1"
	}
	stmts := []string{
		`CREATE SEQUENCE ` + name + `_seq`,
		fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF %s THEN
        RAISE EXCEPTION 'simulated crash before the event';
    END IF;
    RETURN NEW;
END $$`, name, condition),
		fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON meeting_events FOR EACH ROW
WHEN (NEW.meeting_id = '%s'::uuid AND NEW.type = 'agenda_changed') EXECUTE FUNCTION %s()`, name, meetingID, name),
	}
	for _, stmt := range stmts {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	drop := func() {
		_, _ = pool.Exec(ctx, `DROP TRIGGER IF EXISTS `+name+` ON meeting_events`)
		_, _ = pool.Exec(ctx, `DROP FUNCTION IF EXISTS `+name+`()`)
		_, _ = pool.Exec(ctx, `DROP SEQUENCE IF EXISTS `+name+`_seq`)
	}
	t.Cleanup(drop)
	return drop
}

func TestWithTheFlagOffChangesRecordTheirEventAtInsert(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-off-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-off")
	source := serving("text/plain", plainAgenda(agendaV1))
	model := &countingModel{}
	enabled := func(_ context.Context, name string) bool {
		return name == jobs.FlagCheckAgencies || name == jobs.FlagSummarizeAgendas
	}
	h := start(t, pool, jobs.Config{Fetcher: source, LLM: model.fake(), Enabled: enabled})
	agendaURL := "https://agency.test/change-off/agenda.txt"

	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(3)), "completed")
	source.set("text/plain", plainAgenda(agendaV2))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(2)), "completed")
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished, store.EventAgendaChanged) {
		t.Fatalf("events right after the insert = %v", got)
	}
	source.set("text/markdown", markdownAgenda(agendaV2))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(1)), "completed")

	versions := versionsOf(t, pool, meeting.ID)
	if len(versions) != 3 {
		t.Fatalf("versions = %d, want 3: a formatting change is a version with the flag off", len(versions))
	}
	for _, v := range versions {
		if v.ChangeEventPending || v.ChangeNote != nil {
			t.Errorf("version %d pending %v, note %v", v.Version, v.ChangeEventPending, v.ChangeNote)
		}
	}
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished, store.EventAgendaChanged, store.EventAgendaChanged) {
		t.Errorf("events = %v", got)
	}
	for _, p := range changePayloads(t, pool, meeting.ID) {
		if p.ChangeNote != nil || p.NoteSource != "" || p.FoundAt == nil {
			t.Errorf("payload = %+v, want found_at and no note", p)
		}
	}
	if n := len(h.describeJobs()); n != 0 || model.changes.Load() != 0 {
		t.Errorf("describe jobs = %d, change calls = %d", n, model.changes.Load())
	}
}

func TestTheFlagTurnedOffAndOnBetweenInsertAndJobKeepsEventsInOrder(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-toggle-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-toggle")
	agendaV3 := append(append([]string(nil), agendaV2...), "15. Adjourn to the closed session")
	source := serving("text/plain", plainAgenda(agendaV1))
	model := &countingModel{}
	flag := &switchableFlag{}
	h := start(t, pool, jobs.Config{Fetcher: source, LLM: model.fake(), Enabled: flag.enabled})
	agendaURL := "https://agency.test/change-toggle/agenda.txt"

	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(3)), "completed")
	v2 := addDeferred(t, pool, meeting.ID, agendaV2)

	flag.set(false)
	source.set("text/plain", plainAgenda(agendaV3))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(1)), "completed")
	v3 := versionsOf(t, pool, meeting.ID)[0]
	if v3.Version != 3 || !v3.ChangeEventPending {
		t.Fatalf("version 3 = %+v, want pending behind version 2 with the flag off", v3)
	}
	v3Job := h.describeJobOf(v3.ID)
	testdb.WaitForJobState(t, pool, v3Job, "scheduled")
	testdb.WaitForJobState(t, pool, h.summarizeJobOf(v3.ID), "completed")
	if v, err := store.New(pool).GetAgendaVersion(t.Context(), v3.ID); err != nil || v.Summary == nil || !v.ChangeEventPending {
		t.Errorf("version 3 = (summary %v, pending %v, %v), want summarized while its event waits", v.Summary, v.ChangeEventPending, err)
	}
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished) {
		t.Fatalf("events while version 2 is pending = %v", got)
	}

	testdb.WaitForJobState(t, pool, h.describe(v2.ID, nil), "completed")
	flag.set(true)
	wake(t, pool, v3Job)
	testdb.WaitForJobState(t, pool, v3Job, "completed")

	payloads := changePayloads(t, pool, meeting.ID)
	if len(payloads) != 2 || payloads[0].Version != 2 || payloads[1].Version != 3 {
		t.Fatalf("change events = %+v, want version 2 then 3", payloads)
	}
	if payloads[0].NoteSource != store.NoteFromDiff || payloads[1].NoteSource != store.NoteFromModel {
		t.Errorf("note sources = %q, %q, want diff (flag off) then model", payloads[0].NoteSource, payloads[1].NoteSource)
	}
	if !payloads[1].FoundAt.Equal(v3.CreatedAt) || model.changes.Load() != 1 {
		t.Errorf("found_at = %v, want %v; change calls = %d", payloads[1].FoundAt, v3.CreatedAt, model.changes.Load())
	}
}

func TestAStuckEarlierVersionIsRecordedFirstAfterTheWait(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-stuck-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-stuck")
	model := &countingModel{}
	h := start(t, pool, jobs.Config{LLM: model.fake()})

	addDeferred(t, pool, meeting.ID, agendaV1)
	v2 := addDeferred(t, pool, meeting.ID, agendaV2)
	v3 := addDeferred(t, pool, meeting.ID, append(append([]string(nil), agendaV2...), "15. Closed session"))
	if _, err := pool.Exec(t.Context(), `UPDATE agenda_versions SET created_at = $2 WHERE id = $1`, v2.ID, fixedNow.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	testdb.WaitForJobState(t, pool, h.describe(v3.ID, nil), "completed")

	payloads := changePayloads(t, pool, meeting.ID)
	if len(payloads) != 2 || payloads[0].Version != 2 || payloads[0].NoteSource != store.NoteFromDiff ||
		payloads[1].Version != 3 || payloads[1].NoteSource != store.NoteFromModel {
		t.Errorf("change events = %+v, want version 2 from the diff, then version 3", payloads)
	}
}

func TestACrashBetweenTheModelCallAndTheRecordCostsNoSecondCall(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-crash-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-crash")
	model := &countingModel{}
	h := start(t, pool, jobs.Config{LLM: model.fake()})

	addDeferred(t, pool, meeting.ID, agendaV1)
	v2 := addDeferred(t, pool, meeting.ID, agendaV2)
	failChangeEvents(t, pool, meeting.ID, true)
	id := h.describe(v2.ID, nil)
	h.waitForError(id)
	testdb.WaitForJobState(t, pool, id, "completed")

	payloads := changePayloads(t, pool, meeting.ID)
	if len(payloads) != 1 || payloads[0].NoteSource != store.NoteFromDiff || model.changes.Load() != 1 {
		t.Errorf("change events = %+v, change calls = %d, want one event from the diff after one model call", payloads, model.changes.Load())
	}
}

func TestTheSweepRecoversADiscardedJobWithoutTheModel(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-discard-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-discard")
	model := &countingModel{}
	h := start(t, pool, jobs.Config{LLM: model.fake()})

	addDeferred(t, pool, meeting.ID, agendaV1)
	v2 := addDeferred(t, pool, meeting.ID, agendaV2)
	drop := failChangeEvents(t, pool, meeting.ID, false)
	discarded := h.describe(v2.ID, &river.InsertOpts{MaxAttempts: 1})
	testdb.WaitForJobState(t, pool, discarded, "discarded")
	drop()
	if _, err := pool.Exec(t.Context(), `UPDATE agenda_versions SET created_at = $2 WHERE id = $1`, v2.ID, fixedNow.Add(-4*time.Hour)); err != nil {
		t.Fatal(err)
	}

	res, err := h.client.Insert(t.Context(), jobs.EnqueueAgendaChangesArgs{Slot: fixedNow}, &river.InsertOpts{Queue: h.queue})
	if err != nil {
		t.Fatal(err)
	}
	testdb.WaitForJobState(t, pool, res.Job.ID, "completed")
	jobsNow := h.describeJobs()
	if len(jobsNow) != 2 {
		t.Fatalf("describe jobs after the sweep = %d, want 2", len(jobsNow))
	}
	for _, j := range jobsNow {
		if j.ID != discarded {
			testdb.WaitForJobState(t, pool, j.ID, "completed")
		}
	}

	payloads := changePayloads(t, pool, meeting.ID)
	if len(payloads) != 1 || payloads[0].NoteSource != store.NoteFromDiff || model.changes.Load() != 1 {
		t.Errorf("change events = %+v, change calls = %d, want the event from the diff and no second model call", payloads, model.changes.Load())
	}
}
