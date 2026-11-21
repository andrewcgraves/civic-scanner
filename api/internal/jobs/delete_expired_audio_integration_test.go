//go:build integration

package jobs_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

var retentionNow = time.Date(1700, 6, 1, 10, 0, 0, 0, time.UTC)

type retentionRecording struct {
	expiresAt   time.Time
	transcribed bool
	inBucket    bool
}

func seedRetentionRecording(t *testing.T, pool *pgxpool.Pool, bucket storage.Store, name string, r retentionRecording) store.Recording {
	t.Helper()
	ctx := t.Context()
	s := store.New(pool)
	agencyID := seedAgency(t, pool, "jobs-test-retention-"+name+"-"+randomSuffix(t), "live")
	rec := seedRecording(t, pool, agencyID, name)
	key := jobs.AudioKey(rec.ID)
	if r.inBucket {
		if err := bucket.Put(ctx, key, bytes.NewReader(fakeMP3)); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := s.SetRecordingAudio(ctx, rec.ID, store.RecordingAudio{Key: key, ExpiresAt: r.expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	if r.transcribed {
		if _, _, err := s.SaveTranscript(ctx, store.TranscriptInput{RecordingID: rec.ID, Text: "Call to order."}); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

func (h *harness) deleteExpiredAudio() int64 {
	h.t.Helper()
	res, err := h.client.Insert(h.t.Context(), jobs.DeleteExpiredAudioArgs{Slot: retentionNow}, &river.InsertOpts{Queue: h.queue})
	if err != nil {
		h.t.Fatalf("insert delete expired audio job: %v", err)
	}
	return res.Job.ID
}

func TestDeleteExpiredAudioDeletesOnlyTranscribedAudioPastItsExpiry(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := t.Context()
	s := store.New(pool)
	bucket := storagetest.New()
	expired := retentionNow.Add(-time.Hour)

	deleted := seedRetentionRecording(t, pool, bucket, "expired", retentionRecording{expiresAt: expired, transcribed: true, inBucket: true})
	untranscribed := seedRetentionRecording(t, pool, bucket, "untranscribed", retentionRecording{expiresAt: expired, inBucket: true})
	fresh := seedRetentionRecording(t, pool, bucket, "fresh", retentionRecording{expiresAt: retentionNow.Add(time.Hour), transcribed: true, inBucket: true})
	missing := seedRetentionRecording(t, pool, bucket, "missing", retentionRecording{expiresAt: expired, transcribed: true})

	h := start(t, pool, jobs.Config{Storage: bucket, Clock: sourcestest.NewClock(retentionNow)})
	testdb.WaitForJobState(t, pool, h.deleteExpiredAudio(), "completed")

	for _, rec := range []store.Recording{deleted, missing} {
		got, err := s.GetRecording(ctx, rec.ID)
		if err != nil || got.Audio != nil || got.AudioDeletedAt == nil || got.SourceURL != rec.SourceURL {
			t.Errorf("recording %s = %+v, %v; want its key cleared, marked deleted and its URL kept", rec.ID, got, err)
		}
		if _, err := s.GetTranscript(ctx, rec.ID); err != nil {
			t.Errorf("transcript of %s after retention: %v", rec.ID, err)
		}
	}
	if _, err := bucket.Stat(ctx, deleted.Audio.Key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expired object after retention: %v, want ErrNotFound", err)
	}
	for _, rec := range []store.Recording{untranscribed, fresh} {
		got, err := s.GetRecording(ctx, rec.ID)
		if err != nil || got.Audio == nil || got.Audio.Key != rec.Audio.Key || got.AudioDeletedAt != nil {
			t.Errorf("recording %s = %+v, %v; want its audio kept", rec.ID, got, err)
		}
		if _, err := bucket.Stat(ctx, rec.Audio.Key); err != nil {
			t.Errorf("object of %s after retention: %v, want it kept", rec.ID, err)
		}
	}

	detail, err := s.GetLiveMeetingDetail(ctx, deleted.MeetingID)
	if err != nil || len(detail.Recordings) != 1 {
		t.Fatalf("meeting detail = %+v, %v", detail, err)
	}
	if got := detail.Recordings[0]; got.Recording.Audio != nil || got.Recording.SourceURL != deleted.SourceURL || got.Transcript == nil {
		t.Errorf("meeting detail recording = %+v, want no audio, the source URL and the transcript", got)
	}

	downloader := &fakeDownloader{}
	h2 := start(t, pool, jobs.Config{Storage: bucket, Audio: downloader})
	testdb.WaitForJobState(t, pool, h2.downloadAudio(deleted.ID, day(0)), "completed")
	if calls := downloader.calls(); len(calls) != 0 {
		t.Errorf("downloads after retention = %q, want none", calls)
	}
	if _, err := bucket.Stat(ctx, deleted.Audio.Key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("object after a download job for deleted audio: %v, want ErrNotFound", err)
	}
}

func TestDeleteExpiredAudioKeepsTheKeyWhenTheBucketFails(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := t.Context()
	s := store.New(pool)
	rec := seedRetentionRecording(t, pool, storagetest.New(), "bucket-error",
		retentionRecording{expiresAt: retentionNow.Add(-time.Hour), transcribed: true})
	boom := &storage.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}

	h := start(t, pool, jobs.Config{Storage: storagetest.Failing(boom), Clock: sourcestest.NewClock(retentionNow)})
	job := h.waitForError(h.deleteExpiredAudio())

	if job.State == "cancelled" || job.State == "discarded" {
		t.Errorf("job state = %s, want it retried", job.State)
	}
	got, err := s.GetRecording(ctx, rec.ID)
	if err != nil || got.Audio == nil || got.Audio.Key != rec.Audio.Key || got.AudioDeletedAt != nil {
		t.Errorf("recording after a bucket error = %+v, %v; want its key kept", got, err)
	}
}
