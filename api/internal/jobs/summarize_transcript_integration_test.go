//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe/transcribetest"
)

var spokenMeeting = transcribe.Transcript{
	Duration: 20 * time.Second,
	Segments: []transcribe.Segment{
		{End: 5 * time.Second, Text: "Call to order. The board of directors of the transit agency is now in session."},
		{Start: 5 * time.Second, End: 20 * time.Second, Text: "Item four, the fare increase, passes seven to two. Public comment follows on bus service."},
	},
}

func longMeeting(segments int) transcribe.Transcript {
	t := transcribe.Transcript{Duration: time.Duration(segments) * 10 * time.Second}
	for i := range segments {
		t.Segments = append(t.Segments, transcribe.Segment{
			Start: time.Duration(i) * 10 * time.Second,
			End:   time.Duration(i+1) * 10 * time.Second,
			Text:  fmt.Sprintf("Segment %05d.", i) + strings.Repeat(" The board discusses the budget.", 3),
		})
	}
	return t
}

func (h *harness) transcriptSummaryJobs(want int) []*rivertype.JobRow {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := h.client.JobList(h.t.Context(),
			river.NewJobListParams().Queues(h.queue).Kinds(jobs.SummarizeTranscriptArgs{}.Kind()).First(10000))
		if err != nil {
			h.t.Fatalf("list jobs: %v", err)
		}
		if len(res.Jobs) >= want || time.Now().After(deadline) {
			if len(res.Jobs) != want {
				h.t.Fatalf("transcript summary jobs = %d, want %d", len(res.Jobs), want)
			}
			return res.Jobs
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) summarizeTranscript(id uuid.UUID) int64 {
	h.t.Helper()
	res, err := h.client.Insert(h.t.Context(), jobs.SummarizeTranscriptArgs{TranscriptID: id}, &river.InsertOpts{Queue: h.queue})
	if err != nil || res.UniqueSkippedAsDuplicate {
		h.t.Fatalf("insert transcript summary job = (%+v, %v)", res, err)
	}
	return res.Job.ID
}

func (h *harness) sweepTranscriptSummaries(slot time.Time) {
	h.t.Helper()
	res, err := h.client.Insert(h.t.Context(), jobs.EnqueueTranscriptSummariesArgs{Slot: slot}, &river.InsertOpts{Queue: h.queue})
	if err != nil || res.UniqueSkippedAsDuplicate {
		h.t.Fatalf("insert transcript summary sweep = (%+v, %v)", res, err)
	}
	testdb.WaitForJobState(h.t, h.pool, res.Job.ID, "completed")
}

func transcriptEvents(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) []store.TranscriptEventPayload {
	t.Helper()
	events, err := store.New(pool).ListMeetingEvents(t.Context(), meetingID)
	if err != nil {
		t.Fatal(err)
	}
	var payloads []store.TranscriptEventPayload
	for _, e := range events {
		if e.Type != store.EventTranscriptReady {
			t.Errorf("unexpected event %s", e.Type)
			continue
		}
		var p store.TranscriptEventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, p)
	}
	return payloads
}

func transcribedRecording(t *testing.T, pool *pgxpool.Pool, h *harness, rec store.Recording) store.Transcript {
	t.Helper()
	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	tr, ok := transcriptOf(t, pool, rec.ID)
	if !ok {
		t.Fatal("no transcript saved")
	}
	return tr
}

func TestATranscriptIsSummarizedOnceAndMarkedReadyInTheSameTransaction(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "summary")
	fake := llmtest.Returning("The board approved the fare increase 7 to 2.")
	h := start(t, pool, jobs.Config{
		Storage:       bucket,
		LLM:           fake,
		SummaryTier:   llm.TierStrong,
		Transcription: jobs.Transcription{Client: transcribetest.Returning(spokenMeeting)},
	})

	tr := transcribedRecording(t, pool, h, rec)
	queued := h.transcriptSummaryJobs(1)
	testdb.WaitForJobState(t, pool, queued[0].ID, "completed")

	detail, err := store.New(pool).GetLiveMeetingDetail(t.Context(), rec.MeetingID)
	if err != nil {
		t.Fatal(err)
	}
	got := detail.Recordings[0].Transcript
	if got == nil || got.Summary == nil || *got.Summary != "The board approved the fare increase 7 to 2." {
		t.Fatalf("meeting detail transcript = %+v", got)
	}
	if events := transcriptEvents(t, pool, rec.MeetingID); len(events) != 1 || events[0].TranscriptID != tr.ID || events[0].RecordingID != rec.ID {
		t.Errorf("transcript_ready events = %+v, want one for the transcript", events)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Tier != llm.TierRoutine {
		t.Fatalf("calls = %+v, want one on the routine tier whatever the agenda tier", calls)
	}

	testdb.WaitForJobState(t, pool, h.summarizeTranscript(tr.ID), "completed")
	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	if len(fake.Calls()) != 1 || len(transcriptEvents(t, pool, rec.MeetingID)) != 1 {
		t.Errorf("re-runs: calls %d, events %d, want no second call and no second event", len(fake.Calls()), len(transcriptEvents(t, pool, rec.MeetingID)))
	}
	h.transcriptSummaryJobs(2)
}

func TestALongTranscriptIsSummarizedInPartsAndAFailedPartStoresNothing(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "summary-parts")
	var (
		n     atomic.Int32
		mu    sync.Mutex
		users []string
	)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	fake := &llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		mu.Lock()
		users = append(users, req.User)
		mu.Unlock()
		switch n.Add(1) {
		case 2:
			return llm.Response{}, &llm.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}
		case 3:
			<-release
		}
		if strings.HasPrefix(req.User, "Summarize this meeting from the notes") {
			return llm.Response{Text: "The board spent the whole meeting on the budget.", FinishReason: "stop"}, nil
		}
		return llm.Response{Text: fmt.Sprintf("Notes from call %d.", n.Load()), FinishReason: "stop"}, nil
	}}
	h := start(t, pool, jobs.Config{
		Storage:       bucket,
		LLM:           fake,
		Transcription: jobs.Transcription{Client: transcribetest.Returning(longMeeting(2400))},
	})
	t.Cleanup(releaseAll)

	tr := transcribedRecording(t, pool, h, rec)
	queued := h.transcriptSummaryJobs(1)
	job := h.waitForError(queued[0].ID)
	if !strings.Contains(job.Errors[0].Error, "part 2 of 3") {
		t.Errorf("first error = %q", job.Errors[0].Error)
	}
	if got, _ := transcriptOf(t, pool, rec.ID); got.Summary != nil || len(transcriptEvents(t, pool, rec.MeetingID)) != 0 {
		t.Errorf("after a failed part: summary %v, want none and no event", got.Summary)
	}
	releaseAll()
	testdb.WaitForJobState(t, pool, queued[0].ID, "completed")

	got, _ := transcriptOf(t, pool, rec.ID)
	if got.Summary == nil || *got.Summary != "The board spent the whole meeting on the budget." {
		t.Errorf("summary = %v", got.Summary)
	}
	if events := transcriptEvents(t, pool, rec.MeetingID); len(events) != 1 || events[0].TranscriptID != tr.ID {
		t.Errorf("events = %+v", events)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(users) != 6 {
		t.Fatalf("model calls = %d, want 2 on the failed attempt and 4 on the retry", len(users))
	}
	for i, part := range []int{1, 2, 1, 2, 3} {
		if want := fmt.Sprintf("Take notes on part %d of 3 of this meeting", part); !strings.HasPrefix(users[i], want) {
			t.Errorf("call %d = %.60q, want %q", i+1, users[i], want)
		}
	}
	if !strings.HasPrefix(users[5], "Summarize this meeting from the notes on its 3 parts.") || !strings.Contains(users[5], "Part 3 of 3, from ") {
		t.Errorf("combine call = %.200q", users[5])
	}
}

func TestATranscriptCommitsWhenItsSummaryInsertFailsInTheDatabase(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "summary-hook-fail")
	fake := llmtest.Returning("never")
	h := start(t, pool, jobs.Config{
		Storage:       bucket,
		LLM:           fake,
		Transcription: jobs.Transcription{Client: transcribetest.Returning(spokenMeeting)},
	})
	fn := "fail_transcript_summary_insert_" + h.queue
	ctx := context.WithoutCancel(t.Context())
	if _, err := pool.Exec(ctx, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.queue = '`+h.queue+`' AND NEW.kind = 'summarize_transcript' THEN
    RAISE EXCEPTION 'transcript summary insert refused by test trigger';
  END IF;
  RETURN NEW;
END $$`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DROP FUNCTION IF EXISTS `+fn+`() CASCADE`) })
	if _, err := pool.Exec(ctx, `CREATE TRIGGER `+fn+` BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	id := h.transcribe(rec.ID)
	testdb.WaitForJobState(t, pool, id, "completed")

	if _, ok := transcriptOf(t, pool, rec.ID); !ok {
		t.Fatal("the transcript was rolled back by a failed summary insert")
	}
	if msg := lastError(t, h, id); msg != "" {
		t.Errorf("transcribe job recorded an error %q, want it to succeed on its first attempt", msg)
	}
	h.transcriptSummaryJobs(0)
	if len(fake.Calls()) != 0 || len(transcriptEvents(t, pool, rec.MeetingID)) != 0 {
		t.Errorf("calls %d, events %d, want none until the sweep", len(fake.Calls()), len(transcriptEvents(t, pool, rec.MeetingID)))
	}
}

func TestEachRecordingOfAMeetingIsMarkedReadyOnce(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	first := recordingWithAudio(t, pool, bucket, "summary-two")
	second, _, err := store.New(pool).UpsertRecording(t.Context(), store.RecordingInput{MeetingID: first.MeetingID, SourceURL: recordingPage + "&part=2"})
	if err != nil {
		t.Fatal(err)
	}
	second = storeAudio(t, pool, bucket, second, fakeMP3)
	fake := llmtest.Returning("Summary.")
	h := start(t, pool, jobs.Config{Storage: bucket, LLM: fake, Transcription: jobs.Transcription{Client: transcribetest.Returning(spokenMeeting)}})

	transcribedRecording(t, pool, h, first)
	transcribedRecording(t, pool, h, second)
	for _, j := range h.transcriptSummaryJobs(2) {
		testdb.WaitForJobState(t, pool, j.ID, "completed")
	}

	events := transcriptEvents(t, pool, first.MeetingID)
	if len(events) != 2 || events[0].RecordingID == events[1].RecordingID || len(fake.Calls()) != 2 {
		t.Errorf("events = %+v, calls %d, want one per recording", events, len(fake.Calls()))
	}
}

func TestTheTranscriptSummarySweepSummarizesTranscriptsLeftWithoutOne(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	owed := recordingWithAudio(t, pool, bucket, "summary-sweep-owed")
	refused := recordingWithAudio(t, pool, bucket, "summary-sweep-refused")
	silent := recordingWithAudio(t, pool, bucket, "summary-sweep-silent")
	now := time.Date(2091, 1, 1, 13, 0, 0, 0, time.UTC).AddDate(0, 0, int(time.Now().UnixNano()%3000))
	for i, rec := range []store.Recording{owed, refused, silent} {
		if _, err := pool.Exec(t.Context(), `UPDATE meetings SET starts_at = $2 WHERE id = $1`, rec.MeetingID, now.AddDate(0, 0, -1).Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	old := recordingWithAudio(t, pool, bucket, "summary-sweep-old")
	if _, err := pool.Exec(t.Context(), `UPDATE meetings SET starts_at = $2 WHERE id = $1`, old.MeetingID, now.AddDate(-1, 0, -2)); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	fake := &llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		calls.Add(1)
		if strings.Contains(req.User, "refuse") {
			return llm.Response{}, &llm.Error{StatusCode: 400, Err: errors.New("bad request")}
		}
		return llm.Response{Text: "Swept summary.", FinishReason: "stop"}, nil
	}}
	enabled := false
	var mu sync.Mutex
	h := start(t, pool, jobs.Config{
		Storage: bucket,
		LLM:     fake,
		Clock:   sourcestest.NewClock(now),
		Enabled: func(_ context.Context, name string) bool {
			mu.Lock()
			defer mu.Unlock()
			return enabled || name != jobs.FlagSummarizeTranscripts
		},
	})
	st := store.New(pool)
	save := func(rec store.Recording, text string) store.Transcript {
		tr, _, err := st.SaveTranscript(t.Context(), store.TranscriptInput{
			RecordingID: rec.ID, Text: text, Segments: []store.TranscriptSegment{{End: time.Minute, Text: text}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	speech := strings.Repeat("The board debates the bus network redesign. ", 5)
	owedT := save(owed, speech)
	refusedT := save(refused, speech+"Please refuse this one.")
	silentT := save(silent, "Thank you.")
	oldT := save(old, speech)

	h.sweepTranscriptSummaries(time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC))
	h.transcriptSummaryJobs(0)

	mu.Lock()
	enabled = true
	mu.Unlock()
	h.sweepTranscriptSummaries(time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC))
	for _, j := range h.transcriptSummaryJobs(3) {
		want := "completed"
		if strings.Contains(string(j.EncodedArgs), refusedT.ID.String()) {
			want = "cancelled"
		}
		testdb.WaitForJobState(t, pool, j.ID, want)
	}
	for _, c := range []struct {
		tr   store.Transcript
		want string
	}{{owedT, "Swept summary."}, {silentT, "The recording has too little speech to summarize."}, {refusedT, ""}, {oldT, ""}} {
		got, err := st.GetTranscriptByID(t.Context(), c.tr.ID)
		if err != nil || (c.want == "") != (got.Summary == nil) || (got.Summary != nil && *got.Summary != c.want) {
			t.Errorf("transcript %s: summary %v (%v), want %q", c.tr.ID, got.Summary, err, c.want)
		}
	}
	if events := transcriptEvents(t, pool, refused.MeetingID); len(events) != 0 {
		t.Errorf("refused transcript events = %+v, want none", events)
	}
	if len(transcriptEvents(t, pool, owed.MeetingID)) != 1 || len(transcriptEvents(t, pool, silent.MeetingID)) != 1 {
		t.Error("the summarized transcripts must each be marked ready once")
	}
	if calls.Load() != 2 {
		t.Errorf("model calls = %d, want 2: the silent transcript calls none", calls.Load())
	}

	h.sweepTranscriptSummaries(time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC))
	h.transcriptSummaryJobs(3)
	if calls.Load() != 2 {
		t.Errorf("model calls = %d, want the refused transcript backed off", calls.Load())
	}
}
