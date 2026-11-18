//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/media"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

const recordingPage = "https://metrolink.granicus.com/MediaPlayer.php?view_id=8&clip_id=1438"

var fakeMP3 = append([]byte("ID3"), bytes.Repeat([]byte{0xff, 0xfb, 0x90, 0x00}, 64)...)

type fakeDownloader struct {
	mu     sync.Mutex
	urls   []string
	err    error
	before func(ctx context.Context)
}

func (d *fakeDownloader) DownloadAudio(ctx context.Context, recordingURL, dst string) (media.Audio, error) {
	d.mu.Lock()
	d.urls = append(d.urls, recordingURL)
	err, before := d.err, d.before
	d.mu.Unlock()
	if before != nil {
		before(ctx)
	}
	if err != nil {
		return media.Audio{}, err
	}
	if err := os.WriteFile(dst, fakeMP3, 0o600); err != nil {
		return media.Audio{}, err
	}
	return media.Audio{MediaURL: "https://archive-stream.granicus.com/x/playlist.m3u8", Via: media.ViaPlayer, Bytes: int64(len(fakeMP3))}, nil
}

func (d *fakeDownloader) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.urls...)
}

type recordingAudioFollowUp struct {
	mu   sync.Mutex
	recs []store.Recording
	err  error
}

func (f *recordingAudioFollowUp) AudioStored(_ context.Context, _ pgx.Tx, r store.Recording) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, r)
	return f.err
}

func (f *recordingAudioFollowUp) got() []store.Recording {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Recording(nil), f.recs...)
}

func heldWithRecording(id string) sources.Meeting {
	m := boardMeeting(id, fixedNow.AddDate(0, 0, -7), sources.StatusHeld)
	m.RecordingURL = recordingPage + "&m=" + id
	return m
}

func (h *harness) downloadJobs() []*rivertype.JobRow {
	h.t.Helper()
	res, err := h.client.JobList(h.t.Context(),
		river.NewJobListParams().Queues(h.audioQueue).Kinds(jobs.DownloadAudioArgs{}.Kind()).First(10000))
	if err != nil {
		h.t.Fatalf("list jobs: %v", err)
	}
	return res.Jobs
}

func (h *harness) downloadAudio(recordingID uuid.UUID, day time.Time) int64 {
	h.t.Helper()
	res, err := h.client.Insert(h.t.Context(), jobs.DownloadAudioArgs{RecordingID: recordingID},
		&river.InsertOpts{Queue: h.audioQueue, ScheduledAt: day})
	if err != nil {
		h.t.Fatalf("insert download audio job: %v", err)
	}
	if res.UniqueSkippedAsDuplicate {
		h.t.Fatalf("download audio job on %s skipped as duplicate", day)
	}
	return res.Job.ID
}

func recordingsOf(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) []store.Recording {
	t.Helper()
	recs, err := store.New(pool).ListRecordings(t.Context(), meetingID)
	if err != nil {
		t.Fatalf("list recordings: %v", err)
	}
	return recs
}

func seedRecording(t *testing.T, pool *pgxpool.Pool, agencyID uuid.UUID, sourceID string) store.Recording {
	t.Helper()
	m := seedMeeting(t, pool, agencyID, sourceID)
	r, _, err := store.New(pool).UpsertRecording(t.Context(), store.RecordingInput{MeetingID: m.ID, SourceURL: recordingPage + "&m=" + sourceID})
	if err != nil {
		t.Fatalf("upsert recording: %v", err)
	}
	return r
}

func objectBytes(t *testing.T, bucket storage.Store, key string) []byte {
	t.Helper()
	r, err := bucket.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheckEnqueuesDownloadAudioForHeldMeetingsAndStoresTheAudioOnce(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-audio-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	held := heldWithRecording("held")
	upcoming := boardMeeting("upcoming", fixedNow.AddDate(0, 0, 7), sources.StatusScheduled)
	upcoming.RecordingURL = recordingPage + "&m=upcoming"
	bucket := storagetest.New()
	downloader := &fakeDownloader{}
	followUp := &recordingAudioFollowUp{}
	fake := sourcestest.Returning(held, upcoming)
	h := start(t, pool, jobs.Config{
		Registry:      registry(map[string]sources.Adapter{slug: fake}),
		Storage:       bucket,
		Audio:         downloader,
		AudioFollowUp: followUp,
	})

	h.checkAndWait(slug, day(2), "completed")
	meetings := meetingsOf(t, pool, agencyID)
	queued := h.downloadJobs()
	if len(queued) != 1 {
		t.Fatalf("download jobs = %d, want 1 (held meeting only)", len(queued))
	}
	testdb.WaitForJobState(t, pool, queued[0].ID, "completed")

	if recs := recordingsOf(t, pool, meetings["upcoming"].ID); len(recs) != 0 {
		t.Errorf("recordings of a scheduled meeting = %+v", recs)
	}
	recs := recordingsOf(t, pool, meetings["held"].ID)
	if len(recs) != 1 {
		t.Fatalf("recordings of the held meeting = %d", len(recs))
	}
	rec := recs[0]
	var args jobs.DownloadAudioArgs
	if err := json.Unmarshal(queued[0].EncodedArgs, &args); err != nil || args.RecordingID != rec.ID {
		t.Errorf("job args = %+v, %v; want recording %s", args, err, rec.ID)
	}
	key := jobs.AudioKey(rec.ID)
	wantExpiry := fixedNow.AddDate(0, 6, 0)
	if rec.SourceURL != held.RecordingURL || rec.Audio == nil || rec.Audio.Key != key || !rec.Audio.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("recording = %+v (audio %+v), want key %s expiring %s", rec, rec.Audio, key, wantExpiry)
	}
	if got := objectBytes(t, bucket, key); !bytes.Equal(got, fakeMP3) {
		t.Errorf("stored %d bytes, want the downloaded audio", len(got))
	}
	if calls := downloader.calls(); len(calls) != 1 || calls[0] != held.RecordingURL {
		t.Errorf("downloads = %q", calls)
	}
	if got := followUp.got(); len(got) != 1 || got[0].ID != rec.ID || got[0].Audio == nil {
		t.Errorf("audio follow-up = %+v", got)
	}

	h.checkAndWait(slug, day(1), "completed")
	if queued := h.downloadJobs(); len(queued) != 1 {
		t.Errorf("download jobs after a second check = %d, want still 1", len(queued))
	}
	testdb.WaitForJobState(t, pool, h.downloadAudio(rec.ID, day(3)), "completed")
	if calls := downloader.calls(); len(calls) != 1 {
		t.Errorf("downloads after a repeat run = %d, want 1", len(calls))
	}
	if got := followUp.got(); len(got) != 1 {
		t.Errorf("audio follow-ups after a repeat run = %d, want 1", len(got))
	}
}

func TestAudioDeletedByRetentionIsNeverDownloadedAgain(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-audio-retention-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	rec := seedRecording(t, pool, agencyID, "old")
	s := store.New(pool)
	key := jobs.AudioKey(rec.ID)
	if _, err := s.SetRecordingAudio(t.Context(), rec.ID, store.RecordingAudio{Key: key, ExpiresAt: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRecordingAudioDeleted(t.Context(), rec.ID, key); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE meetings SET status = 'held' WHERE id = $1`, rec.MeetingID); err != nil {
		t.Fatal(err)
	}
	downloader := &fakeDownloader{}
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: sourcestest.Returning(heldWithRecording("old"))}),
		Storage:  storagetest.New(),
		Audio:    downloader,
	})

	h.checkAndWait(slug, day(1), "completed")
	if queued := h.downloadJobs(); len(queued) != 0 {
		t.Errorf("download jobs for deleted audio = %d, want 0", len(queued))
	}
	testdb.WaitForJobState(t, pool, h.downloadAudio(rec.ID, day(0)), "completed")
	if calls := downloader.calls(); len(calls) != 0 {
		t.Errorf("downloads = %q, want none", calls)
	}
	if got, _ := s.GetRecording(t.Context(), rec.ID); got.Audio != nil || got.AudioDeletedAt == nil {
		t.Errorf("recording = %+v, want it to stay deleted", got)
	}
}

func TestCheckWithTheAudioFlagOffAddsNoRecordings(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-audio-flag-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	downloader := &fakeDownloader{}
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: sourcestest.Returning(heldWithRecording("held"))}),
		Enabled:  func(_ context.Context, flag string) bool { return flag != jobs.FlagDownloadAudio },
		Storage:  storagetest.New(),
		Audio:    downloader,
	})

	h.checkAndWait(slug, day(0), "completed")
	if recs := recordingsOf(t, pool, meetingsOf(t, pool, agencyID)["held"].ID); len(recs) != 0 {
		t.Errorf("recordings with the flag off = %+v", recs)
	}
	if queued := h.downloadJobs(); len(queued) != 0 {
		t.Errorf("download jobs with the flag off = %d", len(queued))
	}

	rec := seedRecording(t, pool, agencyID, "manual")
	testdb.WaitForJobState(t, pool, h.downloadAudio(rec.ID, day(0)), "completed")
	if calls := downloader.calls(); len(calls) != 0 {
		t.Errorf("downloads with the flag off = %q", calls)
	}
}

func TestDownloadAudioFailures(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-audio-fail-"+randomSuffix(t), "live")
	cases := map[string]struct {
		storage   storage.Store
		err       error
		state     string
		downloads int
	}{
		"no media in the player":   {storagetest.New(), media.ErrNoMedia, "cancelled", 1},
		"bot check on the player":  {storagetest.New(), media.ErrBlocked, "retryable", 1},
		"media host refused":       {storagetest.New(), media.ErrRefused, "cancelled", 1},
		"network failure":          {storagetest.New(), media.ErrNetwork, "retryable", 1},
		"storage not configured":   {storagetest.Failing(storage.ErrNotConfigured), nil, "cancelled", 0},
		"storage refuses":          {storagetest.Failing(&minioLikeError{}), nil, "cancelled", 0},
		"upload failed in transit": {&failingPut{Fake: storagetest.New(), err: context.DeadlineExceeded}, nil, "retryable", 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := seedRecording(t, pool, agencyID, "fail-"+randomSuffix(t))
			downloader := &fakeDownloader{err: tc.err}
			h := start(t, pool, jobs.Config{Storage: tc.storage, Audio: downloader})
			id := h.downloadAudio(rec.ID, day(0))
			if tc.state == "retryable" {
				if job := h.waitForError(id); !retrying(job) {
					t.Errorf("state = %s, want a retry", job.State)
				}
			} else {
				testdb.WaitForJobState(t, pool, id, tc.state)
			}
			if calls := downloader.calls(); len(calls) != tc.downloads {
				t.Errorf("downloads = %d, want %d", len(calls), tc.downloads)
			}
			if got, _ := store.New(pool).GetRecording(t.Context(), rec.ID); got.Audio != nil {
				t.Errorf("audio recorded after a failure: %+v", got.Audio)
			}
		})
	}
}

func retrying(job *rivertype.JobRow) bool {
	return job.State == rivertype.JobStateRetryable || job.State == rivertype.JobStateAvailable || job.State == rivertype.JobStateRunning
}

type minioLikeError struct{}

func (*minioLikeError) Error() string { return "access denied" }

type failingPut struct {
	*storagetest.Fake
	err error
}

func (f *failingPut) Put(context.Context, string, io.Reader) error { return f.err }

func TestDownloadAudioForAMissingRecordingIsCancelled(t *testing.T) {
	pool := testdb.Pool(t)
	h := start(t, pool, jobs.Config{Storage: storagetest.New(), Audio: &fakeDownloader{}})
	testdb.WaitForJobState(t, pool, h.downloadAudio(uuid.New(), day(0)), "cancelled")
}

func TestDownloadAudioRecordsAnUploadLeftByAnEarlierRun(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-audio-left-"+randomSuffix(t), "live")
	rec := seedRecording(t, pool, agencyID, "left")
	bucket := storagetest.New()
	if err := bucket.Put(t.Context(), jobs.AudioKey(rec.ID), bytes.NewReader(fakeMP3)); err != nil {
		t.Fatal(err)
	}
	downloader := &fakeDownloader{}
	h := start(t, pool, jobs.Config{Storage: bucket, Audio: downloader})

	testdb.WaitForJobState(t, pool, h.downloadAudio(rec.ID, day(0)), "completed")
	if calls := downloader.calls(); len(calls) != 0 {
		t.Errorf("downloads = %d, want 0", len(calls))
	}
	if got, _ := store.New(pool).GetRecording(t.Context(), rec.ID); got.Audio == nil || got.Audio.Key != jobs.AudioKey(rec.ID) {
		t.Errorf("recording audio = %+v", got.Audio)
	}
}

func TestDownloadAudioRemovesItsUploadWhenRetentionDeletedTheAudioMeanwhile(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-audio-race-"+randomSuffix(t), "live")
	rec := seedRecording(t, pool, agencyID, "race")
	s := store.New(pool)
	key := jobs.AudioKey(rec.ID)
	bucket := storagetest.New()
	downloader := &fakeDownloader{before: func(ctx context.Context) {
		if _, err := s.SetRecordingAudio(ctx, rec.ID, store.RecordingAudio{Key: key, ExpiresAt: fixedNow}); err != nil {
			t.Error(err)
		}
		if _, err := s.MarkRecordingAudioDeleted(ctx, rec.ID, key); err != nil {
			t.Error(err)
		}
	}}
	h := start(t, pool, jobs.Config{Storage: bucket, Audio: downloader})

	testdb.WaitForJobState(t, pool, h.downloadAudio(rec.ID, day(0)), "completed")
	if _, err := bucket.Stat(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("upload kept after retention deleted the audio: %v", err)
	}
	if got, _ := s.GetRecording(t.Context(), rec.ID); got.Audio != nil || got.AudioDeletedAt == nil {
		t.Errorf("recording = %+v, want it to stay deleted", got)
	}
}

func TestDownloadAudioKeepsAudioAnotherRunStoredMeanwhile(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-audio-dup-"+randomSuffix(t), "live")
	rec := seedRecording(t, pool, agencyID, "dup")
	s := store.New(pool)
	key := jobs.AudioKey(rec.ID)
	followUp := &recordingAudioFollowUp{}
	downloader := &fakeDownloader{before: func(ctx context.Context) {
		if _, err := s.AddRecordingAudio(ctx, rec.ID, store.RecordingAudio{Key: key, ExpiresAt: fixedNow}); err != nil {
			t.Error(err)
		}
	}}
	h := start(t, pool, jobs.Config{Storage: storagetest.New(), Audio: downloader, AudioFollowUp: followUp})

	testdb.WaitForJobState(t, pool, h.downloadAudio(rec.ID, day(0)), "completed")
	if got := followUp.got(); len(got) != 0 {
		t.Errorf("follow-up ran for audio stored by another run: %+v", got)
	}
	if got, _ := s.GetRecording(t.Context(), rec.ID); got.Audio == nil || !got.Audio.ExpiresAt.Equal(fixedNow) {
		t.Errorf("recording audio = %+v, want the other run's", got.Audio)
	}
}

func TestDownloadAudioFollowUpErrorRollsBackTheAudio(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-audio-followup-"+randomSuffix(t), "live")
	rec := seedRecording(t, pool, agencyID, "followup")
	followUp := &recordingAudioFollowUp{err: errors.New("enqueue transcription failed")}
	h := start(t, pool, jobs.Config{Storage: storagetest.New(), Audio: &fakeDownloader{}, AudioFollowUp: followUp})

	job := h.waitForError(h.downloadAudio(rec.ID, day(0)))
	if !retrying(job) || !strings.Contains(job.Errors[0].Error, "enqueue transcription failed") {
		t.Errorf("job = %s %+v", job.State, job.Errors)
	}
	if got, _ := store.New(pool).GetRecording(t.Context(), rec.ID); got.Audio != nil {
		t.Errorf("audio kept after the follow-up failed: %+v", got.Audio)
	}
}

func realBucket(t *testing.T) storage.Store {
	t.Helper()
	bucket, err := storage.New(config.Storage{
		Endpoint:        os.Getenv("STORAGE_ENDPOINT"),
		Region:          "auto",
		Bucket:          os.Getenv("STORAGE_BUCKET"),
		AccessKeyID:     os.Getenv("STORAGE_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("STORAGE_SECRET_ACCESS_KEY"),
		PathStyle:       os.Getenv("STORAGE_PATH_STYLE") == "true",
	})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	return bucket
}

func TestDownloadAudioFromAGranicusStyleHLSStreamIntoTheBucket(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{
			"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=15", "-f", "lavfi", "-i", "sine=frequency=330:duration=15",
			"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", "clip.mp4",
		},
		{"-i", "clip.mp4", "-c", "copy", "-f", "hls", "-hls_time", "5", "-hls_playlist_type", "vod", "playlist.m3u8"},
	} {
		cmd := exec.CommandContext(t.Context(), "ffmpeg", append([]string{"-nostdin", "-v", "error", "-y"}, args...)...) //nolint:gosec
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, out)
		}
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	player := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/MediaPlayer.php?view_id=8&clip_id=77"
	pageFetcher := &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		body := `<script>video_url="` + srv.URL + `/playlist.m3u8"</script>`
		return fetch.Response{Status: 200, ContentType: "text/html", Body: []byte(body), FinalURL: req.URL}, nil
	}}
	downloader := media.Downloader{
		Resolver: media.Resolver{Fetcher: pageFetcher, Hosts: media.Hosts{Players: []string{"localhost"}, Media: []string{"127.0.0.1"}}},
		Tool: media.Tool{
			Limit:     testbin.BuildPackage(t, "../../cmd/limit"),
			Timeout:   time.Minute,
			UserAgent: "CivicScanner-test",
			Env:       testbin.CoverEnv(),
		},
	}

	pool := testdb.Pool(t)
	slug := "jobs-test-audio-e2e-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	meeting := boardMeeting("e2e", fixedNow.AddDate(0, 0, -3), sources.StatusHeld)
	meeting.RecordingURL = player
	bucket := realBucket(t)
	h := start(t, pool, jobs.Config{
		Registry: registry(map[string]sources.Adapter{slug: sourcestest.Returning(meeting)}),
		Storage:  bucket,
		Audio:    downloader,
	})

	h.checkAndWait(slug, day(0), "completed")
	queued := h.downloadJobs()
	if len(queued) != 1 {
		t.Fatalf("download jobs = %d, want 1", len(queued))
	}
	testdb.WaitForJobState(t, pool, queued[0].ID, "completed")
	recs := recordingsOf(t, pool, meetingsOf(t, pool, agencyID)["e2e"].ID)
	if len(recs) != 1 || recs[0].Audio == nil {
		t.Fatalf("recordings = %+v", recs)
	}
	key := recs[0].Audio.Key
	t.Cleanup(func() { _ = bucket.Delete(context.WithoutCancel(t.Context()), key) })
	stored := objectBytes(t, bucket, key)
	path := filepath.Join(t.TempDir(), "stored.mp3")
	if err := os.WriteFile(path, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	if size, err := media.CheckMP3(path, media.DefaultMinBytes); err != nil || size > 100_000 {
		t.Errorf("stored audio = %d bytes, %v; want 15 s of speech-grade mp3", size, err)
	}
}
