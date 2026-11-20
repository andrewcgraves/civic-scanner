//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type errorCounter struct {
	mu     sync.Mutex
	errors []slog.Record
}

func (c *errorCounter) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelError
}

func (c *errorCounter) Handle(_ context.Context, rec slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors = append(c.errors, rec.Clone())
	return nil
}

func (c *errorCounter) WithAttrs([]slog.Attr) slog.Handler { return c }

func (c *errorCounter) WithGroup(string) slog.Handler { return c }

func (c *errorCounter) accountFailures() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, rec := range c.errors {
		if rec.Message == jobs.LLMAccountFailureMessage {
			n++
		}
	}
	return n
}

type switchableLLM struct {
	broken atomic.Bool
	calls  atomic.Int32
	text   string
}

func (s *switchableLLM) fake() *llmtest.Fake {
	return &llmtest.Fake{Respond: func(llm.Request) (llm.Response, error) {
		s.calls.Add(1)
		if s.broken.Load() {
			return llm.Response{}, &llm.Error{
				StatusCode: 403, Account: true, Provider: "gateway", Model: "anthropic/claude-haiku-5.5",
				Type: "permission_error", Err: errors.New("gateway returned 403: model not available on this plan"),
			}
		}
		return llm.Response{Text: s.text, FinishReason: "stop", Model: "fake/model"}, nil
	}}
}

func (h *harness) jobRow(id int64) *rivertype.JobRow {
	h.t.Helper()
	row, err := h.client.JobGet(h.t.Context(), id)
	if err != nil {
		h.t.Fatalf("get job %d: %v", id, err)
	}
	return row
}

func (h *harness) wakeSnoozed(id int64) {
	h.t.Helper()
	if _, err := h.client.JobRetry(h.t.Context(), id); err != nil {
		h.t.Fatalf("retry job %d: %v", id, err)
	}
}

func TestAnAccountFailureLeavesTranscriptsPendingAndTheyAreSummarizedOnceItIsFixed(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	first := recordingWithAudio(t, pool, bucket, "account-first")
	second := recordingWithAudio(t, pool, bucket, "account-second")
	now := time.Date(2191, 1, 1, 13, 0, 0, 0, time.UTC).AddDate(0, 0, int(time.Now().UnixNano()%300000))
	for i, rec := range []store.Recording{first, second} {
		if _, err := pool.Exec(t.Context(), `UPDATE meetings SET starts_at = $2 WHERE id = $1`, rec.MeetingID, now.AddDate(0, 0, -1).Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	model := &switchableLLM{text: "The board debated the bus network."}
	model.broken.Store(true)
	logs := &errorCounter{}
	clock := sourcestest.NewClock(now)
	h := startLogged(t, pool, jobs.Config{Storage: bucket, LLM: model.fake(), Clock: clock}, slog.New(logs))
	st := store.New(pool)
	speech := strings.Repeat("The board debates the bus network redesign. ", 5)
	var transcripts []store.Transcript
	for _, rec := range []store.Recording{first, second} {
		tr, _, err := st.SaveTranscript(t.Context(), store.TranscriptInput{
			RecordingID: rec.ID, Text: speech, Segments: []store.TranscriptSegment{{End: time.Minute, Text: speech}},
		})
		if err != nil {
			t.Fatal(err)
		}
		transcripts = append(transcripts, tr)
	}

	firstJob := h.summarizeTranscript(transcripts[0].ID)
	testdb.WaitForJobState(t, pool, firstJob, "scheduled")
	if model.calls.Load() != 1 || logs.accountFailures() != 1 {
		t.Fatalf("calls %d, error lines %d, want 1 and 1", model.calls.Load(), logs.accountFailures())
	}
	if row := h.jobRow(firstJob); row.Attempt != 0 || row.ScheduledAt.Before(time.Now().Add(50*time.Minute)) {
		t.Errorf("snoozed job attempt %d scheduled %v, want no attempt spent and about an hour out", row.Attempt, row.ScheduledAt)
	}

	h.sweepTranscriptSummaries(time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC))
	queued := h.transcriptSummaryJobs(2)
	var secondJob int64
	for _, j := range queued {
		if j.ID != firstJob {
			secondJob = j.ID
		}
	}
	testdb.WaitForJobState(t, pool, secondJob, "scheduled")
	if model.calls.Load() != 1 || logs.accountFailures() != 1 {
		t.Errorf("during the cool-down: calls %d, error lines %d, want still 1 and 1", model.calls.Load(), logs.accountFailures())
	}
	h.sweepTranscriptSummaries(time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC))
	h.transcriptSummaryJobs(2)

	model.broken.Store(false)
	clock.Advance(10 * time.Minute)
	for _, id := range []int64{firstJob, secondJob} {
		h.wakeSnoozed(id)
		testdb.WaitForJobState(t, pool, id, "completed")
	}
	for i, tr := range transcripts {
		got, err := st.GetTranscriptByID(t.Context(), tr.ID)
		if err != nil || got.Summary == nil || *got.Summary != model.text {
			t.Errorf("transcript %d summary = %v (%v)", i, got.Summary, err)
		}
	}
	for _, rec := range []store.Recording{first, second} {
		if n := len(transcriptEvents(t, pool, rec.MeetingID)); n != 1 {
			t.Errorf("transcript_ready events = %d, want 1", n)
		}
	}
	if row := h.jobRow(firstJob); row.Attempt != 1 || len(row.Errors) != 0 {
		t.Errorf("first job attempt %d errors %d, want 1 and none", row.Attempt, len(row.Errors))
	}
	if model.calls.Load() != 3 || logs.accountFailures() != 1 {
		t.Errorf("calls %d, error lines %d, want 3 and 1", model.calls.Load(), logs.accountFailures())
	}

	h.sweepTranscriptSummaries(time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC))
	h.transcriptSummaryJobs(2)
	if model.calls.Load() != 3 {
		t.Errorf("calls = %d after a later sweep, want no more", model.calls.Load())
	}
}

func TestAnAccountFailureLeavesAnAgendaVersionPendingUntilItIsFixed(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-account-"+randomSuffix(t), "live")
	m := seedMeeting(t, pool, agencyID, "account")
	model := &switchableLLM{text: "The board meets on December 4."}
	model.broken.Store(true)
	logs := &errorCounter{}
	clock := sourcestest.NewClock(fixedNow)
	h := startLogged(t, pool, jobs.Config{LLM: model.fake(), Clock: clock, Enabled: allFlagsBut(jobs.FlagAgendaChanges)}, slog.New(logs))

	testdb.WaitForJobState(t, pool, h.processAgenda(m.ID, "https://agency.test/account/agenda.txt", day(1)), "completed")
	job := h.summarizeAndWait(1, "scheduled")[0]
	if v := versionsOf(t, pool, m.ID); len(v) != 1 || v[0].Summary != nil {
		t.Fatalf("versions = %+v, want one without a summary", v)
	}
	if model.calls.Load() != 1 || logs.accountFailures() != 1 {
		t.Fatalf("calls %d, error lines %d, want 1 and 1", model.calls.Load(), logs.accountFailures())
	}

	dup, err := h.client.Insert(t.Context(), jobs.SummarizeAgendaArgs{AgendaVersionID: versionsOf(t, pool, m.ID)[0].ID}, &river.InsertOpts{Queue: h.queue})
	if err != nil || !dup.UniqueSkippedAsDuplicate {
		t.Errorf("insert while snoozed = (%+v, %v), want a skipped duplicate", dup, err)
	}

	model.broken.Store(false)
	clock.Advance(10 * time.Minute)
	h.wakeSnoozed(job.ID)
	testdb.WaitForJobState(t, pool, job.ID, "completed")
	if v := versionsOf(t, pool, m.ID); v[0].Summary == nil || *v[0].Summary != model.text {
		t.Errorf("summary = %v, want the model's", v[0].Summary)
	}
	if !sameTypes(eventTypesOf(t, pool, m.ID), store.EventAgendaPublished) {
		t.Errorf("events = %v, want only the publication", eventTypesOf(t, pool, m.ID))
	}
	if model.calls.Load() != 2 || logs.accountFailures() != 1 {
		t.Errorf("calls %d, error lines %d, want 2 and 1", model.calls.Load(), logs.accountFailures())
	}
	h.summarizeJobs(1)
}
