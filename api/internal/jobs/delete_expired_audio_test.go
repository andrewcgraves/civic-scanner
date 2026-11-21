package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var retentionNow = time.Date(2027, 5, 20, 10, 0, 0, 0, time.UTC)

type fakeRetentionRecording struct {
	rec         store.Recording
	transcribed bool
}

type fakeRetentionStore struct {
	recordings []*fakeRetentionRecording
	listErr    error
	markErr    error
	lists      []int
	marked     []uuid.UUID
}

func (s *fakeRetentionStore) add(expiresAt time.Time, transcribed bool) *fakeRetentionRecording {
	id := uuid.New()
	r := &fakeRetentionRecording{
		rec: store.Recording{
			ID:        id,
			SourceURL: "https://metro.granicus.com/player/clip/" + id.String(),
			Audio:     &store.RecordingAudio{Key: AudioKey(id), ExpiresAt: expiresAt},
		},
		transcribed: transcribed,
	}
	s.recordings = append(s.recordings, r)
	return r
}

func (s *fakeRetentionStore) ListTranscribedRecordingsWithExpiredAudio(_ context.Context, now time.Time, limit int) ([]store.Recording, error) {
	s.lists = append(s.lists, limit)
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []store.Recording
	for _, r := range s.recordings {
		if r.rec.Audio != nil && r.transcribed && !r.rec.Audio.ExpiresAt.After(now) && len(out) < limit {
			out = append(out, r.rec)
		}
	}
	return out, nil
}

func (s *fakeRetentionStore) MarkRecordingAudioDeleted(_ context.Context, id uuid.UUID, key string) (store.Recording, error) {
	if s.markErr != nil {
		return store.Recording{}, s.markErr
	}
	for _, r := range s.recordings {
		if r.rec.ID != id {
			continue
		}
		if r.rec.Audio == nil || r.rec.Audio.Key != key {
			return store.Recording{}, store.ErrAudioChanged
		}
		deletedAt := retentionNow
		r.rec.Audio = nil
		r.rec.AudioDeletedAt = &deletedAt
		s.marked = append(s.marked, id)
		return r.rec, nil
	}
	return store.Recording{}, store.ErrNotFound
}

func retentionFlag(on bool) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool { return on && name == FlagAudioRetention }
}

func retentionWorker(st *fakeRetentionStore, bucket storage.Store) *DeleteExpiredAudioWorker {
	return &DeleteExpiredAudioWorker{
		logger:  discard,
		store:   st,
		storage: bucket,
		clock:   sourcestest.NewClock(retentionNow),
		enabled: retentionFlag(true),
	}
}

func retentionJob() *river.Job[DeleteExpiredAudioArgs] {
	return &river.Job[DeleteExpiredAudioArgs]{JobRow: &rivertype.JobRow{ID: 11}}
}

func putAudio(t *testing.T, bucket *storagetest.Fake, r *fakeRetentionRecording) {
	t.Helper()
	if err := bucket.Put(t.Context(), r.rec.Audio.Key, strings.NewReader("audio")); err != nil {
		t.Fatal(err)
	}
}

func inBucket(t *testing.T, bucket storage.Store, key string) bool {
	t.Helper()
	_, err := bucket.Stat(t.Context(), key)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestDeleteExpiredAudioArgs(t *testing.T) {
	args := DeleteExpiredAudioArgs{}
	if kind := args.Kind(); kind != "delete_expired_audio" {
		t.Errorf("Kind() = %q", kind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 5 || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Errorf("InsertOpts() = %+v", opts)
	}
	if got := (&DeleteExpiredAudioWorker{}).Timeout(retentionJob()); got != 15*time.Minute {
		t.Errorf("Timeout = %v, want 15m", got)
	}
}

func TestPeriodicAudioRetentionTargetsTodaysSlot(t *testing.T) {
	clock := sourcestest.NewClock(time.Date(2027, 5, 20, 9, 59, 59, 0, time.UTC))
	construct := periodicAudioRetention(dailySchedule{hourUTC: audioRetentionHourUTC}, clock, "ingest")

	args, opts := construct()
	want := DeleteExpiredAudioArgs{Slot: time.Date(2027, 5, 20, 10, 0, 0, 0, time.UTC)}
	if args != want || opts.Queue != "ingest" {
		t.Errorf("args = %+v, opts = %+v, want %+v on ingest", args, opts, want)
	}
	clock.Set(time.Date(2027, 5, 21, 3, 0, 0, 0, time.UTC))
	if args, _ := construct(); args != want {
		t.Errorf("before the next day's hour: args = %+v, want %+v", args, want)
	}
}

func TestDeleteExpiredAudioWithTheFlagOffTouchesNothing(t *testing.T) {
	st := &fakeRetentionStore{}
	bucket := storagetest.New()
	r := st.add(retentionNow.Add(-time.Hour), true)
	putAudio(t, bucket, r)
	w := retentionWorker(st, bucket)
	w.enabled = retentionFlag(false)

	if err := w.Work(t.Context(), retentionJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(st.lists) != 0 || r.rec.Audio == nil || !inBucket(t, bucket, AudioKey(r.rec.ID)) {
		t.Errorf("flag off: lists %v, audio %+v", st.lists, r.rec.Audio)
	}
}

func TestDeleteExpiredAudioDeletesOnlyTranscribedAudioPastItsExpiry(t *testing.T) {
	st := &fakeRetentionStore{}
	bucket := storagetest.New()
	expired := st.add(retentionNow.Add(-time.Hour), true)
	exact := st.add(retentionNow, true)
	untranscribed := st.add(retentionNow.AddDate(-1, 0, 0), false)
	fresh := st.add(retentionNow.Add(time.Minute), true)
	for _, r := range st.recordings {
		putAudio(t, bucket, r)
	}
	keys := map[*fakeRetentionRecording]string{}
	for _, r := range st.recordings {
		keys[r] = r.rec.Audio.Key
	}

	if err := retentionWorker(st, bucket).Work(t.Context(), retentionJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}

	for name, r := range map[string]*fakeRetentionRecording{"expired": expired, "exact": exact} {
		if r.rec.Audio != nil || r.rec.AudioDeletedAt == nil || inBucket(t, bucket, keys[r]) {
			t.Errorf("%s: audio %+v, deleted at %v, in bucket %v; want deleted", name, r.rec.Audio, r.rec.AudioDeletedAt, inBucket(t, bucket, keys[r]))
		}
		if !strings.HasPrefix(r.rec.SourceURL, "https://metro.granicus.com/") {
			t.Errorf("%s: source URL = %q, want it kept", name, r.rec.SourceURL)
		}
	}
	for name, r := range map[string]*fakeRetentionRecording{"untranscribed": untranscribed, "fresh": fresh} {
		if r.rec.Audio == nil || r.rec.AudioDeletedAt != nil || !inBucket(t, bucket, keys[r]) {
			t.Errorf("%s: audio %+v, in bucket %v; want kept", name, r.rec.Audio, inBucket(t, bucket, keys[r]))
		}
	}
}

func TestDeleteExpiredAudioClearsTheKeyWhenTheObjectIsAlreadyGone(t *testing.T) {
	st := &fakeRetentionStore{}
	r := st.add(retentionNow.Add(-time.Hour), true)

	if err := retentionWorker(st, storagetest.New()).Work(t.Context(), retentionJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if r.rec.Audio != nil || r.rec.AudioDeletedAt == nil {
		t.Errorf("audio = %+v, want the key cleared", r.rec.Audio)
	}
}

type notFoundDelete struct{ *storagetest.Fake }

func (notFoundDelete) Delete(_ context.Context, key string) error {
	return fmt.Errorf("%w: %q", storage.ErrNotFound, key)
}

func TestDeleteExpiredAudioTreatsNotFoundFromTheBucketAsDeleted(t *testing.T) {
	st := &fakeRetentionStore{}
	r := st.add(retentionNow.Add(-time.Hour), true)

	if err := retentionWorker(st, notFoundDelete{storagetest.New()}).Work(t.Context(), retentionJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if r.rec.Audio != nil || r.rec.AudioDeletedAt == nil {
		t.Errorf("audio = %+v, want the key cleared", r.rec.Audio)
	}
}

func TestDeleteExpiredAudioKeepsTheKeyWhenTheBucketFails(t *testing.T) {
	retryable := &storage.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}
	permanent := &storage.Error{StatusCode: 403, Err: errors.New("access denied")}
	for name, tc := range map[string]struct {
		err    error
		cancel bool
	}{
		"retryable": {retryable, false},
		"permanent": {permanent, true},
	} {
		t.Run(name, func(t *testing.T) {
			st := &fakeRetentionStore{}
			r := st.add(retentionNow.Add(-time.Hour), true)

			err := retentionWorker(st, storagetest.Failing(tc.err)).Work(t.Context(), retentionJob())

			if !errors.Is(err, tc.err) || isCancel(err) != tc.cancel {
				t.Errorf("Work = %v, want %v with cancel %v", err, tc.err, tc.cancel)
			}
			if r.rec.Audio == nil || r.rec.AudioDeletedAt != nil || len(st.marked) != 0 {
				t.Errorf("audio = %+v, marked %v; want the key kept", r.rec.Audio, st.marked)
			}
		})
	}
}

func TestDeleteExpiredAudioRetriesAfterACrashBetweenTheDeleteAndTheKeyClear(t *testing.T) {
	st := &fakeRetentionStore{markErr: errors.New("connection reset")}
	bucket := storagetest.New()
	r := st.add(retentionNow.Add(-time.Hour), true)
	putAudio(t, bucket, r)
	w := retentionWorker(st, bucket)

	if err := w.Work(t.Context(), retentionJob()); err == nil || isCancel(err) {
		t.Fatalf("Work = %v, want a retryable error", err)
	}
	if r.rec.Audio == nil || inBucket(t, bucket, AudioKey(r.rec.ID)) {
		t.Fatalf("after the failed clear: audio %+v, want the key kept and the object gone", r.rec.Audio)
	}

	st.markErr = nil
	if err := w.Work(t.Context(), retentionJob()); err != nil {
		t.Fatalf("retry: Work = %v", err)
	}
	if r.rec.Audio != nil || r.rec.AudioDeletedAt == nil {
		t.Errorf("after the retry: audio %+v, want the key cleared", r.rec.Audio)
	}
}

func TestDeleteExpiredAudioToleratesARecordingThatChangedMeanwhile(t *testing.T) {
	for name, markErr := range map[string]error{"audio changed": store.ErrAudioChanged, "gone": store.ErrNotFound} {
		t.Run(name, func(t *testing.T) {
			st := &fakeRetentionStore{markErr: markErr}
			st.add(retentionNow.Add(-time.Hour), true)
			st.add(retentionNow.Add(-time.Hour), true)

			if err := retentionWorker(st, storagetest.New()).Work(t.Context(), retentionJob()); err != nil {
				t.Errorf("Work = %v, want nil", err)
			}
		})
	}
}

func TestDeleteExpiredAudioWorksInBoundedBatches(t *testing.T) {
	st := &fakeRetentionStore{}
	total := audioRetentionBatchSize*audioRetentionMaxBatches + 5
	for range total {
		st.add(retentionNow.Add(-time.Hour), true)
	}

	if err := retentionWorker(st, storagetest.New()).Work(t.Context(), retentionJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(st.lists) != audioRetentionMaxBatches || len(st.marked) != audioRetentionBatchSize*audioRetentionMaxBatches {
		t.Errorf("lists = %d, marked = %d; want %d batches of %d", len(st.lists), len(st.marked), audioRetentionMaxBatches, audioRetentionBatchSize)
	}
	for _, limit := range st.lists {
		if limit != audioRetentionBatchSize {
			t.Errorf("batch limit = %d, want %d", limit, audioRetentionBatchSize)
		}
	}

	if err := retentionWorker(st, storagetest.New()).Work(t.Context(), retentionJob()); err != nil {
		t.Fatalf("next run: Work: %v", err)
	}
	if len(st.marked) != total {
		t.Errorf("marked after the next run = %d, want %d", len(st.marked), total)
	}
}

func TestDeleteExpiredAudioRetriesStoreErrors(t *testing.T) {
	boom := errors.New("connection reset")
	err := retentionWorker(&fakeRetentionStore{listErr: boom}, storagetest.New()).Work(t.Context(), retentionJob())
	if !errors.Is(err, boom) || isCancel(err) {
		t.Errorf("Work = %v, want the retryable error", err)
	}
}
