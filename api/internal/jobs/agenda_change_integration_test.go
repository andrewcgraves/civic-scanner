//go:build integration

package jobs_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/agendadiff"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/summarize"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func markdownAgenda(lines []string) string {
	var b strings.Builder
	b.WriteString("# " + lines[0] + "\n\n**" + lines[1] + "**\n\n")
	for _, l := range lines[2:] {
		b.WriteString("- " + strings.Replace(l, ". ", "\\. ", 1) + "\n")
	}
	return b.String() + "\nPage 1 of 1\n"
}

func (h *harness) describeJobs() []*rivertype.JobRow {
	h.t.Helper()
	res, err := h.client.JobList(h.t.Context(),
		river.NewJobListParams().Queues(h.queue).Kinds(jobs.DescribeAgendaChangeArgs{}.Kind()).First(100))
	if err != nil {
		h.t.Fatalf("list jobs: %v", err)
	}
	return res.Jobs
}

func (h *harness) describeAndWait(want int) []*rivertype.JobRow {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		found := h.describeJobs()
		if len(found) == want {
			for _, j := range found {
				testdb.WaitForJobState(h.t, h.pool, j.ID, "completed")
			}
			return found
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("describe jobs = %d, want %d", len(found), want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func agendaEvents(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) []store.MeetingEvent {
	t.Helper()
	events, err := store.New(pool).ListMeetingEvents(t.Context(), meetingID)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func isChangeRequest(req llm.Request) bool {
	return strings.Contains(req.User, "\n<changes-")
}

func TestAFormattingOnlyChangeStoresNothingAndRecordsNoEvent(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-noise-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-noise")
	source := serving("application/octet-stream", plainAgenda(agendaV1))
	followUp := &recordingAgendaFollowUp{}
	fake := llmtest.Returning("never")
	h := start(t, pool, jobs.Config{Fetcher: source, AgendaFollowUp: followUp, LLM: fake})
	agendaURL := "https://agency.test/change-noise/agenda.pdf"

	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(2)), "completed")
	source.set("text/markdown", markdownAgenda(agendaV1))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(1)), "completed")

	versions := versionsOf(t, pool, meeting.ID)
	if len(versions) != 1 || versions[0].Text != strings.Join(agendaV1, "\n") {
		t.Fatalf("versions = %+v, want only the first", versions)
	}
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished) {
		t.Errorf("events = %v, want only agenda_published", got)
	}
	if got := followUp.got(); len(got) != 1 {
		t.Errorf("follow-up calls = %d, want 1: no summary for a formatting change", len(got))
	}
	if n := len(h.describeJobs()); n != 0 || len(fake.Calls()) != 0 {
		t.Errorf("describe jobs = %d, model calls = %d, want none", n, len(fake.Calls()))
	}
}

func TestARealChangeRecordsItsEventWithTheNoteOnceDescribed(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-real-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-real")
	source := serving("text/plain", plainAgenda(agendaV1))
	var changeCalls atomic.Int32
	fake := &llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		if isChangeRequest(req) {
			changeCalls.Add(1)
			return llm.Response{Text: "Item 14 adds the 2027 board meeting calendar.", FinishReason: "stop", Model: "fake/model"}, nil
		}
		return llm.Response{Text: "Summary.", FinishReason: "stop"}, nil
	}}
	h := start(t, pool, jobs.Config{Fetcher: source, LLM: fake})
	agendaURL := "https://agency.test/change-real/agenda.txt"

	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(2)), "completed")
	source.set("text/markdown", markdownAgenda(agendaV2))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(1)), "completed")
	described := h.describeAndWait(1)

	versions := versionsOf(t, pool, meeting.ID)
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}
	v2 := versions[0]
	const note = "Item 14 adds the 2027 board meeting calendar."
	if v2.ChangeNote == nil || *v2.ChangeNote != note || v2.ChangeEventPending {
		t.Errorf("version 2 change note = %v, pending = %v", v2.ChangeNote, v2.ChangeEventPending)
	}
	events := agendaEvents(t, pool, meeting.ID)
	if len(events) != 2 || events[1].Type != store.EventAgendaChanged {
		t.Fatalf("events = %+v, want agenda_published then agenda_changed", events)
	}
	var payload store.AgendaEventPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AgendaVersionID != v2.ID || payload.Version != 2 || payload.ChangeNote == nil || *payload.ChangeNote != note {
		t.Errorf("payload = %s", events[1].Payload)
	}
	var request llm.Request
	for _, c := range fake.Calls() {
		if isChangeRequest(c) {
			request = c
		}
	}
	if !strings.Contains(request.User, "Added: 14. Adopt the 2027 board meeting calendar") || strings.Contains(request.User, "Call to Order") {
		t.Errorf("change request = %q, want only the difference", request.User)
	}

	again, err := h.client.Insert(t.Context(), jobs.DescribeAgendaChangeArgs{AgendaVersionID: v2.ID}, &river.InsertOpts{Queue: h.queue})
	if err != nil || again.UniqueSkippedAsDuplicate || again.Job.ID == described[0].ID {
		t.Fatalf("re-enqueue = (%+v, %v)", again, err)
	}
	testdb.WaitForJobState(t, pool, again.Job.ID, "completed")
	if n := len(agendaEvents(t, pool, meeting.ID)); n != 2 || changeCalls.Load() != 1 {
		t.Errorf("after a re-run: events = %d, change model calls = %d, want 2 and 1", n, changeCalls.Load())
	}
}

func TestWithoutAModelTheNoteComesFromTheWordDiff(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-nollm-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-nollm")
	source := serving("text/plain", plainAgenda(agendaV1))
	h := start(t, pool, jobs.Config{Fetcher: source})
	agendaURL := "https://agency.test/change-nollm/agenda.txt"

	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(2)), "completed")
	source.set("text/plain", plainAgenda(agendaV2))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(1)), "completed")
	h.describeAndWait(1)

	v2 := versionsOf(t, pool, meeting.ID)[0]
	want := summarize.FallbackChangeNote(agendadiff.Compare(strings.Join(agendaV1, "\n"), strings.Join(agendaV2, "\n")))
	if v2.ChangeNote == nil || *v2.ChangeNote != want || !strings.Contains(want, "Added: “14. Adopt the 2027 board meeting calendar”") {
		t.Errorf("change note = %v, want %q", v2.ChangeNote, want)
	}
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished, store.EventAgendaChanged) {
		t.Errorf("events = %v", got)
	}
}

func TestAPermanentModelErrorStillRecordsTheEvent(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-modelerr-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-modelerr")
	source := serving("text/plain", plainAgenda(agendaV1))
	fake := &llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		if isChangeRequest(req) {
			return llm.Response{}, &llm.Error{StatusCode: 400, Err: errors.New("bad request")}
		}
		return llm.Response{Text: "Summary.", FinishReason: "stop"}, nil
	}}
	h := start(t, pool, jobs.Config{Fetcher: source, LLM: fake})
	agendaURL := "https://agency.test/change-modelerr/agenda.txt"

	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(2)), "completed")
	source.set("text/plain", plainAgenda(agendaV2))
	testdb.WaitForJobState(t, pool, h.processAgenda(meeting.ID, agendaURL, day(1)), "completed")
	h.describeAndWait(1)

	v2 := versionsOf(t, pool, meeting.ID)[0]
	if v2.ChangeNote == nil || !strings.HasPrefix(*v2.ChangeNote, "Added: ") || v2.ChangeEventPending {
		t.Errorf("change note = %v, pending = %v", v2.ChangeNote, v2.ChangeEventPending)
	}
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished, store.EventAgendaChanged) {
		t.Errorf("events = %v", got)
	}
}

func TestTheChangeSweepEnqueuesVersionsStillWithoutTheirEvent(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-change-sweep-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "change-sweep")
	st := store.New(pool)
	add := func(lines []string, deferred bool) store.AgendaVersion {
		text := strings.Join(lines, "\n")
		v, _, err := st.AddAgendaVersion(t.Context(), store.AgendaVersionInput{
			MeetingID: meeting.ID, SourceURL: "https://agency.test/change-sweep.pdf", Text: text,
			ContentHash: store.HashContent(text), DeferChangeEvent: deferred,
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	add(agendaV1, true)
	stale := add(agendaV2, true)
	if !stale.ChangeEventPending {
		t.Fatal("version 2 is not pending")
	}
	h := start(t, pool, jobs.Config{})

	sweep := func(slot time.Time) {
		res, err := h.client.Insert(t.Context(), jobs.EnqueueAgendaChangesArgs{Slot: slot}, &river.InsertOpts{Queue: h.queue})
		if err != nil || res.UniqueSkippedAsDuplicate {
			t.Fatalf("insert sweep = (%+v, %v)", res, err)
		}
		testdb.WaitForJobState(t, pool, res.Job.ID, "completed")
	}
	sweep(fixedNow)
	if n := len(h.describeJobs()); n != 0 {
		t.Fatalf("describe jobs = %d for a version inside the grace period, want 0", n)
	}

	if _, err := pool.Exec(t.Context(), `UPDATE agenda_versions SET created_at = $2 WHERE id = $1`, stale.ID, fixedNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	sweep(fixedNow.Add(time.Hour))
	h.describeAndWait(1)
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished, store.EventAgendaChanged) {
		t.Errorf("events = %v", got)
	}

	sweep(fixedNow.Add(2 * time.Hour))
	if n := len(h.describeJobs()); n != 1 {
		t.Errorf("describe jobs after a sweep with nothing pending = %d, want 1", n)
	}
}

func TestDescribingAMissingVersionIsCancelled(t *testing.T) {
	pool := testdb.Pool(t)
	h := start(t, pool, jobs.Config{})
	res, err := h.client.Insert(t.Context(), jobs.DescribeAgendaChangeArgs{AgendaVersionID: uuid.New()}, &river.InsertOpts{Queue: h.queue})
	if err != nil {
		t.Fatal(err)
	}
	testdb.WaitForJobState(t, pool, res.Job.ID, "cancelled")
}
