//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/media"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe/transcribetest"
)

var meetingTranscript = transcribe.Transcript{
	Text:     "Call to order. Roll call.",
	Duration: 4 * time.Second,
	Segments: []transcribe.Segment{
		{End: 2 * time.Second, Text: "Call to order.", Speaker: "c0/speaker_0"},
		{Start: 2 * time.Second, End: 4 * time.Second, Text: "Roll call."},
	},
}

type recordingTranscriptFollowUp struct {
	mu  sync.Mutex
	got []store.Transcript
	err error
}

func (f *recordingTranscriptFollowUp) TranscriptSaved(_ context.Context, _ pgx.Tx, t store.Transcript) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, t)
	return f.err
}

func (f *recordingTranscriptFollowUp) saved() []store.Transcript {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Transcript(nil), f.got...)
}

func storeAudio(t *testing.T, pool *pgxpool.Pool, bucket storage.Store, rec store.Recording, body []byte) store.Recording {
	t.Helper()
	key := jobs.AudioKey(rec.ID)
	if err := bucket.Put(t.Context(), key, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bucket.Delete(context.WithoutCancel(t.Context()), key) })
	got, err := store.New(pool).SetRecordingAudio(t.Context(), rec.ID, store.RecordingAudio{Key: key, ExpiresAt: fixedNow.AddDate(0, 6, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func recordingWithAudio(t *testing.T, pool *pgxpool.Pool, bucket storage.Store, name string) store.Recording {
	t.Helper()
	agencyID := seedAgency(t, pool, "jobs-test-transcribe-"+name+"-"+randomSuffix(t), "live")
	return storeAudio(t, pool, bucket, seedRecording(t, pool, agencyID, name), fakeMP3)
}

func (h *harness) transcribe(recordingID uuid.UUID) int64 {
	h.t.Helper()
	return h.transcribeWith(recordingID, nil)
}

func (h *harness) transcribeWith(recordingID uuid.UUID, opts *river.InsertOpts) int64 {
	h.t.Helper()
	if opts == nil {
		opts = &river.InsertOpts{}
	}
	opts.Queue = h.audioQueue
	res, err := h.client.Insert(h.t.Context(), jobs.TranscribeAudioArgs{RecordingID: recordingID}, opts)
	if err != nil {
		h.t.Fatalf("insert transcribe job: %v", err)
	}
	if res.UniqueSkippedAsDuplicate {
		h.t.Fatalf("transcribe job for %s skipped as duplicate", recordingID)
	}
	return res.Job.ID
}

func (h *harness) transcribeJobs() []*rivertype.JobRow {
	h.t.Helper()
	res, err := h.client.JobList(h.t.Context(),
		river.NewJobListParams().Queues(h.audioQueue).Kinds(jobs.TranscribeAudioArgs{}.Kind()).First(10000))
	if err != nil {
		h.t.Fatalf("list jobs: %v", err)
	}
	return res.Jobs
}

func transcriptOf(t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID) (store.Transcript, bool) {
	t.Helper()
	tr, err := store.New(pool).GetTranscript(t.Context(), recordingID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Transcript{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return tr, true
}

func countTranscripts(t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM transcripts WHERE recording_id = $1`, recordingID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func lastError(t *testing.T, h *harness, id int64) string {
	t.Helper()
	job, err := h.client.JobGet(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Errors) == 0 {
		return ""
	}
	return job.Errors[len(job.Errors)-1].Error
}

func TestStoredAudioIsTranscribedOnceThroughTheAudioHook(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-transcribe-hook-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	bucket := storagetest.New()
	client := transcribetest.Returning(meetingTranscript)
	followUp := &recordingTranscriptFollowUp{}
	h := start(t, pool, jobs.Config{
		Registry:      registry(map[string]sources.Adapter{slug: sourcestest.Returning(heldWithRecording("held"))}),
		Storage:       bucket,
		Audio:         &fakeDownloader{},
		Transcription: jobs.Transcription{Client: client, FollowUp: followUp},
	})

	h.checkAndWait(slug, day(1), "completed")
	downloads := h.downloadJobs()
	if len(downloads) != 1 {
		t.Fatalf("download jobs = %d", len(downloads))
	}
	testdb.WaitForJobState(t, pool, downloads[0].ID, "completed")
	queued := h.transcribeJobs()
	if len(queued) != 1 {
		t.Fatalf("transcribe jobs = %d, want 1 enqueued with the audio", len(queued))
	}
	testdb.WaitForJobState(t, pool, queued[0].ID, "completed")

	rec := recordingsOf(t, pool, meetingsOf(t, pool, agencyID)["held"].ID)[0]
	tr, ok := transcriptOf(t, pool, rec.ID)
	if !ok || tr.Text != "Call to order. Roll call." || len(tr.Segments) != 2 || tr.Segments[0].Speaker != "c0/speaker_0" {
		t.Fatalf("transcript = %+v, %v", tr, ok)
	}
	if calls := client.Calls(); len(calls) != 1 || filepath.Base(calls[0]) != "audio.mp3" {
		t.Errorf("transcription calls = %q", calls)
	}
	if got := followUp.saved(); len(got) != 1 || got[0].ID != tr.ID {
		t.Errorf("transcript follow-ups = %+v", got)
	}

	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	if calls := client.Calls(); len(calls) != 1 {
		t.Errorf("transcription calls after a re-enqueue = %d, want 1", len(calls))
	}
	if got := followUp.saved(); len(got) != 1 {
		t.Errorf("follow-ups after a re-enqueue = %d, want 1", len(got))
	}
	if n := countTranscripts(t, pool, rec.ID); n != 1 {
		t.Errorf("transcripts = %d", n)
	}
	if events, _ := store.New(pool).ListMeetingEvents(t.Context(), rec.MeetingID); len(events) != 0 {
		t.Errorf("events = %+v, want none: transcript_ready belongs to the summary job", events)
	}
}

func TestTranscriptionWithTheFlagOffIsNeitherEnqueuedNorRun(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-transcribe-off-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	client := transcribetest.Returning(meetingTranscript)
	h := start(t, pool, jobs.Config{
		Registry:      registry(map[string]sources.Adapter{slug: sourcestest.Returning(heldWithRecording("held"))}),
		Storage:       storagetest.New(),
		Audio:         &fakeDownloader{},
		Transcription: jobs.Transcription{Client: client},
		Enabled:       func(_ context.Context, flag string) bool { return flag != jobs.FlagTranscribe },
	})

	h.checkAndWait(slug, day(1), "completed")
	testdb.WaitForJobState(t, pool, h.downloadJobs()[0].ID, "completed")
	if queued := h.transcribeJobs(); len(queued) != 0 {
		t.Errorf("transcribe jobs with the flag off = %d", len(queued))
	}
	rec := recordingsOf(t, pool, meetingsOf(t, pool, agencyID)["held"].ID)[0]
	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	if len(client.Calls()) != 0 {
		t.Errorf("transcription calls with the flag off = %d", len(client.Calls()))
	}
}

func TestTranscriptionWithoutAClientCompletesQuietly(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	unconfigured := transcribe.New(config.Transcription{Provider: config.TranscriptionGateway})
	for name, client := range map[string]transcribe.Client{"none": nil, "no key": unconfigured} {
		t.Run(name, func(t *testing.T) {
			rec := recordingWithAudio(t, pool, bucket, "unconfigured")
			h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: client}})
			testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
			if _, ok := transcriptOf(t, pool, rec.ID); ok {
				t.Error("transcript written without a client")
			}
		})
	}
}

func TestTranscribeSkipsAudioDeletedBeforeTheRun(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "deleted")
	if _, err := store.New(pool).MarkRecordingAudioDeleted(t.Context(), rec.ID, rec.Audio.Key); err != nil {
		t.Fatal(err)
	}
	client := transcribetest.Returning(meetingTranscript)
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: client}})

	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	if len(client.Calls()) != 0 {
		t.Errorf("calls = %d, want none", len(client.Calls()))
	}
}

type deletingBucket struct {
	*storagetest.Fake
	onGet func()
}

func (b *deletingBucket) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if b.onGet != nil {
		b.onGet()
	}
	return b.Fake.Get(ctx, key)
}

func TestTranscribeCompletesQuietlyWhenRetentionDeletesTheAudioBeforeItIsRead(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := &deletingBucket{Fake: storagetest.New()}
	rec := recordingWithAudio(t, pool, bucket, "racing")
	bucket.onGet = func() {
		_ = bucket.Delete(context.Background(), rec.Audio.Key)
		_, _ = store.New(pool).MarkRecordingAudioDeleted(context.Background(), rec.ID, rec.Audio.Key)
	}
	client := transcribetest.Returning(meetingTranscript)
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: client}})

	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	if len(client.Calls()) != 0 {
		t.Errorf("calls = %d, want none for audio that was gone", len(client.Calls()))
	}
	if _, ok := transcriptOf(t, pool, rec.ID); ok {
		t.Error("transcript written for audio deleted before it was read")
	}
}

func TestTranscriptOfACompleteReadIsKeptWhenRetentionDeletesTheAudioMeanwhile(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "read-then-deleted")
	client := &transcribetest.Fake{Respond: func(path string) (transcribe.Transcript, error) {
		if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, fakeMP3) { //nolint:gosec
			return transcribe.Transcript{}, fmt.Errorf("audio file = %d bytes: %w", len(b), err)
		}
		_ = bucket.Delete(context.Background(), rec.Audio.Key)
		_, _ = store.New(pool).MarkRecordingAudioDeleted(context.Background(), rec.ID, rec.Audio.Key)
		return meetingTranscript, nil
	}}
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: client}})

	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	if _, ok := transcriptOf(t, pool, rec.ID); !ok {
		t.Error("transcript of a complete read was not kept")
	}
}

func TestTranscribeCancelsWhenTheBucketLostAudioTheDatabaseStillHas(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "lost")
	if err := bucket.Delete(t.Context(), rec.Audio.Key); err != nil {
		t.Fatal(err)
	}
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: &transcribetest.Fake{}}})

	id := h.transcribe(rec.ID)
	testdb.WaitForJobState(t, pool, id, "cancelled")
	if msg := lastError(t, h, id); !strings.Contains(msg, jobs.TranscribeKindMissingAudio) {
		t.Errorf("error = %q, want the missing_audio kind", msg)
	}
}

func TestTranscribeCancelsAudioOverTheLimitsBeforeCallingTheModel(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	t.Run("bytes", func(t *testing.T) {
		rec := recordingWithAudio(t, pool, bucket, "big")
		client := &transcribetest.Fake{}
		h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: client, MaxAudioBytes: int64(len(fakeMP3) - 1)}})
		id := h.transcribe(rec.ID)
		testdb.WaitForJobState(t, pool, id, "cancelled")
		if msg := lastError(t, h, id); !strings.Contains(msg, jobs.TranscribeKindLimit) || len(client.Calls()) != 0 {
			t.Errorf("error = %q after %d calls, want a limit failure and no call", msg, len(client.Calls()))
		}
	})
	t.Run("duration", func(t *testing.T) {
		rec := recordingWithAudio(t, pool, bucket, "long")
		srv, calls := chunkGateway(t, nil)
		splitter := &transcribetest.FakeSplitter{Total: 9 * time.Hour}
		h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{
			Client: chunkingClient(srv.URL, splitter), MaxDuration: 8 * time.Hour,
		}})
		id := h.transcribe(rec.ID)
		testdb.WaitForJobState(t, pool, id, "cancelled")
		if msg := lastError(t, h, id); !strings.Contains(msg, jobs.TranscribeKindLimit) || len(calls()) != 0 || len(splitter.Extracts()) != 0 {
			t.Errorf("error = %q after %d uploads, want a limit failure before any chunk", msg, len(calls()))
		}
	})
}

func TestTranscriptSavedByAnotherRunIsKept(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "concurrent")
	s := store.New(pool)
	client := &transcribetest.Fake{Respond: func(string) (transcribe.Transcript, error) {
		if _, _, err := s.SaveTranscript(context.Background(), store.TranscriptInput{RecordingID: rec.ID, Text: "the other run"}); err != nil {
			return transcribe.Transcript{}, err
		}
		return meetingTranscript, nil
	}}
	followUp := &recordingTranscriptFollowUp{}
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: client, FollowUp: followUp}})

	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	if tr, _ := transcriptOf(t, pool, rec.ID); tr.Text != "the other run" {
		t.Errorf("transcript = %q, want the one saved first", tr.Text)
	}
	if n := countTranscripts(t, pool, rec.ID); n != 1 || len(followUp.saved()) != 0 {
		t.Errorf("transcripts = %d, follow-ups = %d; want 1 and 0", n, len(followUp.saved()))
	}
}

func TestASecondJobForTheSameRecordingIsADuplicateWhileOneIsWaiting(t *testing.T) {
	pool := testdb.Pool(t)
	h := start(t, pool, jobs.Config{Storage: storagetest.New()})
	id := uuid.New()
	future := &river.InsertOpts{ScheduledAt: time.Now().Add(time.Hour)}
	h.transcribeWith(id, future)

	res, err := h.client.Insert(t.Context(), jobs.TranscribeAudioArgs{RecordingID: id}, &river.InsertOpts{Queue: h.audioQueue})
	if err != nil {
		t.Fatal(err)
	}
	if !res.UniqueSkippedAsDuplicate {
		t.Error("a second job for a waiting recording was inserted")
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM river_job WHERE queue = $1`, h.audioQueue)
	})
}

func TestTranscriptFollowUpErrorRollsBackTheTranscript(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "rollback")
	followUp := &recordingTranscriptFollowUp{err: errors.New("enqueue failed")}
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{
		Client: transcribetest.Returning(meetingTranscript), FollowUp: followUp,
	}})

	id := h.transcribeWith(rec.ID, &river.InsertOpts{MaxAttempts: 1})
	testdb.WaitForJobState(t, pool, id, "discarded")
	if _, ok := transcriptOf(t, pool, rec.ID); ok {
		t.Error("transcript kept after its follow-up failed")
	}
	if len(followUp.saved()) != 1 {
		t.Errorf("follow-ups = %d", len(followUp.saved()))
	}
}

func TestTranscribeCleansModelOutputBeforeStoringIt(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "hostile")
	client := transcribetest.Returning(transcribe.Transcript{
		Text: "ignored",
		Segments: []transcribe.Segment{
			{Start: -time.Second, End: time.Second, Text: "<script>alert(1)</script>\u202e\x1b[31m ok"},
			{Start: time.Second, End: 2 * time.Second, Text: "\u200b\u2060"},
		},
	})
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: client}})

	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")
	tr, _ := transcriptOf(t, pool, rec.ID)
	if len(tr.Segments) != 1 || tr.Segments[0].Start != 0 || tr.Segments[0].Text != "<script>alert(1)</script>[31m ok" || tr.Text != tr.Segments[0].Text {
		t.Errorf("transcript = %+v", tr)
	}
}

func chunkGateway(t *testing.T, fail func(chunk string, n int) int) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil { //nolint:gosec
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		chunk := strings.TrimSpace(string(b))
		mu.Lock()
		calls = append(calls, chunk)
		n := len(calls)
		mu.Unlock()
		if fail != nil {
			if status := fail(chunk, n); status != 0 {
				w.WriteHeader(status)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"duration": 10,
			"segments": []map[string]any{{"start": 1, "end": 3, "text": " " + chunk}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func chunkingClient(url string, splitter transcribe.Splitter) transcribe.Client {
	return transcribe.New(config.Transcription{
		Provider:          config.TranscriptionGroq,
		APIKey:            "gsk-test",
		BaseURL:           url,
		Model:             "whisper-large-v3-turbo",
		Timeout:           5 * time.Second,
		DirectUploadBytes: 64,
		MaxChunkBytes:     1 << 20,
	}, transcribe.WithSplitter(splitter), transcribe.WithChunking(10*time.Second, 2*time.Second))
}

func TestARetryAfterPartialProgressTranscribesOnlyTheChunksLeft(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "partial")
	var once sync.Once
	srv, calls := chunkGateway(t, func(chunk string, _ int) int {
		status := 0
		if chunk == "chunk@16s" {
			once.Do(func() { status = http.StatusBadGateway })
		}
		return status
	})
	splitter := &transcribetest.FakeSplitter{Total: 20 * time.Second}
	followUp := &recordingTranscriptFollowUp{}
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{
		Client: chunkingClient(srv.URL, splitter), Model: "groq/whisper", Concurrency: 2, FollowUp: followUp,
	}})

	id := h.transcribe(rec.ID)
	testdb.WaitForJobState(t, pool, id, "completed")

	tr, ok := transcriptOf(t, pool, rec.ID)
	if !ok || tr.Text != "chunk@0s chunk@8s chunk@16s" {
		t.Fatalf("transcript = %+v, %v", tr, ok)
	}
	counts := map[string]int{}
	for _, c := range calls() {
		counts[c]++
	}
	if counts["chunk@0s"] != 1 || counts["chunk@8s"] != 1 || counts["chunk@16s"] != 2 {
		t.Errorf("uploads = %v, want the failed chunk twice and the others once", counts)
	}
	if job, _ := h.client.JobGet(t.Context(), id); job.Attempt != 2 {
		t.Errorf("attempts = %d, want 2", job.Attempt)
	}
	if keys, _ := bucket.ListOlderThan(t.Context(), time.Now().Add(time.Hour)); len(keys) != 1 || keys[0] != rec.Audio.Key {
		t.Errorf("bucket keys = %v, want only the audio: checkpoints are removed once the transcript is saved", keys)
	}
	if len(followUp.saved()) != 1 {
		t.Errorf("follow-ups = %d", len(followUp.saved()))
	}
}

func TestATransientFailureOnTheLastAttemptIsDiscardedAndBackedOffBySweep(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	rec := recordingWithAudio(t, pool, bucket, "flaky")
	busy := &transcribe.Error{StatusCode: 503, Retryable: true, Err: errors.New("busy")}
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: transcribetest.Failing(busy)}})

	id := h.transcribeWith(rec.ID, &river.InsertOpts{MaxAttempts: 1})
	testdb.WaitForJobState(t, pool, id, "discarded")
	if msg := lastError(t, h, id); !strings.Contains(msg, jobs.TranscribeKindTransient) {
		t.Errorf("error = %q", msg)
	}
	ids, err := store.New(pool).ListUntranscribedRecordings(t.Context(), store.UntranscribedParams{
		FailedKind: jobs.TranscribeAudioArgs{}.Kind(), FailedWithin: 7 * 24 * time.Hour, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range ids {
		if got == rec.ID {
			t.Error("the sweep would retry a recording whose job was discarded this week")
		}
	}
}

func TestTheTranscriptSweepEnqueuesOwedRecordingsWithBackoff(t *testing.T) {
	pool := testdb.Pool(t)
	bucket := storagetest.New()
	s := store.New(pool)
	owed := recordingWithAudio(t, pool, bucket, "owed")
	done := recordingWithAudio(t, pool, bucket, "done")
	if _, _, err := s.SaveTranscript(t.Context(), store.TranscriptInput{RecordingID: done.ID, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	failing := recordingWithAudio(t, pool, bucket, "failing")
	pendingAgency := seedAgency(t, pool, "jobs-test-transcribe-pending-"+randomSuffix(t), "pending")
	notLive := storeAudio(t, pool, bucket, seedRecording(t, pool, pendingAgency, "pending"), fakeMP3)
	noAudio := seedRecording(t, pool, seedAgency(t, pool, "jobs-test-transcribe-noaudio-"+randomSuffix(t), "live"), "none")
	if _, err := pool.Exec(t.Context(), `
INSERT INTO river_job (kind, args, queue, state, finalized_at, max_attempts, attempt)
VALUES ($1, $2, 'elsewhere', 'cancelled', now() - interval '1 day', 3, 1)`,
		jobs.TranscribeAudioArgs{}.Kind(), fmt.Sprintf(`{"recording_id": %q}`, failing.ID)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM river_job WHERE queue = 'elsewhere' AND args->>'recording_id' = $1`, failing.ID.String())
	})
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{Client: &transcribetest.Fake{
		Respond: func(string) (transcribe.Transcript, error) { return transcribe.Transcript{}, errors.New("stop here") },
	}}})

	sweep := func() {
		t.Helper()
		res, err := h.client.Insert(t.Context(), jobs.EnqueueTranscriptionsArgs{Slot: time.Now()},
			&river.InsertOpts{Queue: h.queue})
		if err != nil {
			t.Fatal(err)
		}
		testdb.WaitForJobState(t, pool, res.Job.ID, "completed")
	}
	sweep()
	enqueued := map[uuid.UUID]bool{}
	for _, j := range h.transcribeJobs() {
		var args jobs.TranscribeAudioArgs
		if err := json.Unmarshal(j.EncodedArgs, &args); err != nil {
			t.Fatal(err)
		}
		enqueued[args.RecordingID] = true
	}
	if !enqueued[owed.ID] {
		t.Error("owed recording not enqueued")
	}
	for name, id := range map[string]uuid.UUID{"transcribed": done.ID, "backing off": failing.ID, "not live": notLive.ID, "no audio": noAudio.ID} {
		if enqueued[id] {
			t.Errorf("%s recording enqueued", name)
		}
	}
	if _, err := s.ListUntranscribedRecordings(t.Context(), store.UntranscribedParams{Limit: 0}); !errors.Is(err, store.ErrInvalidLimit) {
		t.Errorf("limit 0 = %v", err)
	}
}

func TestTranscribeRealAudioFromTheBucketWithFFmpegChunks(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=330:duration=25", //nolint:gosec
		"-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k", "-write_xing", "0", "-id3v2_version", "0", "-f", "mp3", filepath.Join(dir, "meeting.mp3"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	audio, err := os.ReadFile(filepath.Join(dir, "meeting.mp3")) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	pool := testdb.Pool(t)
	bucket := realBucket(t)
	agencyID := seedAgency(t, pool, "jobs-test-transcribe-real-"+randomSuffix(t), "live")
	rec := storeAudio(t, pool, bucket, seedRecording(t, pool, agencyID, "real"), audio)
	var (
		mu    sync.Mutex
		sizes []int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20) //nolint:gosec
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		mu.Lock()
		sizes = append(sizes, len(b))
		n := len(sizes)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"duration": 10, "segments": []map[string]any{{"start": 1, "end": 2, "text": fmt.Sprintf(" part %d", n)}}})
	}))
	defer srv.Close()
	splitter := media.NewSplitter(media.Tool{Limit: testbin.BuildPackage(t, "../../cmd/limit"), Timeout: time.Minute, Env: testbin.CoverEnv()})
	h := start(t, pool, jobs.Config{Storage: bucket, Transcription: jobs.Transcription{
		Client: chunkingClient(srv.URL, splitter), Model: "groq/whisper",
	}})

	testdb.WaitForJobState(t, pool, h.transcribe(rec.ID), "completed")

	tr, ok := transcriptOf(t, pool, rec.ID)
	if !ok || tr.Text != "part 1 part 2 part 3" || len(tr.Segments) != 3 || tr.Segments[2].Start != 17*time.Second {
		t.Fatalf("transcript = %+v, %v", tr, ok)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, size := range sizes {
		if size < 10_000 || size > 60_000 {
			t.Errorf("chunk %d = %d bytes, want about 10 s of 32 kbps mp3", i, size)
		}
	}
}
