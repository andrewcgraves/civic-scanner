package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/media"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

func audioJob() *river.Job[DownloadAudioArgs] {
	return &river.Job[DownloadAudioArgs]{
		JobRow: &rivertype.JobRow{ID: 4},
		Args:   DownloadAudioArgs{RecordingID: uuid.New()},
	}
}

func audioFlag(on bool) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool { return on && name == FlagDownloadAudio }
}

type unusedDownloader struct{ t *testing.T }

func (d unusedDownloader) DownloadAudio(context.Context, string, string) (media.Audio, error) {
	d.t.Fatal("downloader called")
	return media.Audio{}, nil
}

func TestDownloadAudioArgs(t *testing.T) {
	args := DownloadAudioArgs{}
	if kind := args.Kind(); kind != "download_audio" {
		t.Errorf("Kind() = %q, want download_audio", kind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 5 || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || opts.UniqueOpts.ByPeriod != 24*time.Hour {
		t.Errorf("InsertOpts() = %+v", opts)
	}
}

func TestDownloadAudioTimeoutLeavesRoomForUpload(t *testing.T) {
	if got := (&DownloadAudioWorker{timeout: time.Hour}).Timeout(audioJob()); got != 75*time.Minute {
		t.Errorf("Timeout = %v, want 75m", got)
	}
}

func TestAudioKey(t *testing.T) {
	id := uuid.MustParse("0f8fad5b-d9cb-469f-a165-70867728950e")
	if got := AudioKey(id); got != "recordings/0f8fad5b-d9cb-469f-a165-70867728950e.mp3" {
		t.Errorf("AudioKey = %q", got)
	}
}

func TestDownloadAudioWithFlagOffDoesNothing(t *testing.T) {
	w := &DownloadAudioWorker{logger: discard, enabled: audioFlag(false), downloader: unusedDownloader{t}}
	if err := w.Work(t.Context(), audioJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
}

func TestDownloadAudioWithoutDownloaderIsCancelled(t *testing.T) {
	w := &DownloadAudioWorker{logger: discard, enabled: audioFlag(true), storage: storagetest.New()}
	if err := w.Work(t.Context(), audioJob()); !isCancel(err) || !errors.Is(err, ErrNoDownloader) {
		t.Errorf("Work = %v, want a cancelled ErrNoDownloader", err)
	}
}

func TestConfigDefaultsToAnUnconfiguredBucket(t *testing.T) {
	cfg := Config{}.withDefaults(store.New(nil))
	if _, err := cfg.Storage.Stat(t.Context(), "k"); !errors.Is(err, storage.ErrNotConfigured) {
		t.Errorf("default storage Stat = %v, want ErrNotConfigured", err)
	}
}

func TestFailurePolicy(t *testing.T) {
	now := time.Date(2026, 11, 18, 13, 5, 0, 0, time.UTC)
	daily, weekly := 12*time.Hour, 156*time.Hour
	recent := now.Add(-24 * time.Hour)
	old := now.Add(-5 * 24 * time.Hour)
	cases := []struct {
		kind     string
		meeting  time.Time
		previous string
		wait     time.Duration
	}{
		{media.KindInvalidURL, recent, "", 0},
		{media.KindUnsupported, recent, "", weekly},
		{media.KindMalformed, recent, "", weekly},
		{media.KindNoAudio, recent, "", weekly},
		{media.KindRefused, recent, "", weekly},
		{media.KindLimit, recent, "", weekly},
		{media.KindNoMedia, recent, "", daily},
		{media.KindNoMedia, time.Time{}, "", daily},
		{media.KindNoMedia, old, "", weekly},
		{media.KindIncomplete, recent, "", daily},
		{media.KindIncomplete, recent, media.KindTransient, daily},
		{media.KindIncomplete, recent, media.KindIncomplete, weekly},
		{media.KindUnavailable, recent, "", daily},
		{media.KindTransient, recent, "", daily},
	}
	for _, tc := range cases {
		if got := PermanentFailure(tc.kind); got != (tc.wait == 0) {
			t.Errorf("PermanentFailure(%s) = %v", tc.kind, got)
		}
		at := retryAfter(tc.kind, failureContext{now: now, meetingStart: tc.meeting, previous: tc.previous})
		if (tc.wait == 0) != (at == nil) || at != nil && at.Sub(now) != tc.wait {
			t.Errorf("retryAfter(%s, meeting %s, previous %q) = %v, want +%s", tc.kind, tc.meeting, tc.previous, at, tc.wait)
		}
	}

	later := now.Add(time.Hour)
	if _, wait := waiting(store.Recording{Download: store.DownloadState{Failure: media.KindRefused, RetryAfter: &later}}, now); !wait {
		t.Error("waiting before retry_after = false")
	}
	if _, wait := waiting(store.Recording{Download: store.DownloadState{Failure: media.KindRefused, RetryAfter: &now}}, later); wait {
		t.Error("waiting after retry_after = true")
	}
	if _, wait := waiting(store.Recording{Download: store.DownloadState{Failure: media.KindInvalidURL}}, later); !wait {
		t.Error("waiting after a permanent failure = false")
	}
	if _, wait := waiting(store.Recording{}, now); wait {
		t.Error("waiting with no failure = true")
	}
}

func TestSweepAudioTempRemovesOnlyOldAudioDirectories(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	for _, name := range []string{AudioTempPrefix + "old", AudioTempPrefix + "new", "other-old"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, AudioTempPrefix+"file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{AudioTempPrefix + "old", "other-old", AudioTempPrefix + "file"} {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := SweepAudioTemp(dir, 2*time.Hour, now)
	if err != nil || removed != 1 {
		t.Fatalf("SweepAudioTemp = %d, %v; want 1", removed, err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != AudioTempPrefix+"file,"+AudioTempPrefix+"new,other-old" {
		t.Errorf("left %v", names)
	}
	if _, err := SweepAudioTemp(filepath.Join(dir, "missing"), time.Hour, now); err == nil {
		t.Error("SweepAudioTemp on a missing directory returned no error")
	}
	if _, err := SweepAudioTemp("", 24*365*time.Hour, now); err != nil {
		t.Errorf("SweepAudioTemp on the default directory = %v", err)
	}
}

func TestStorageErrorsRetryOnlyTransientFailures(t *testing.T) {
	if err := storageError("k", storage.ErrNotConfigured); !isCancel(err) {
		t.Errorf("storageError(not configured) = %v, want cancelled", err)
	}
	if err := storageError("k", context.Canceled); isCancel(err) {
		t.Errorf("storageError(canceled) = %v, want retryable", err)
	}
}

func TestEnqueueDownloadAudioSkipsWithoutWork(t *testing.T) {
	held := store.Meeting{ID: uuid.New(), Status: "held"}
	never := func(*testing.T) func(context.Context, string) bool {
		return func(context.Context, string) bool {
			t.Fatal("flag checked")
			return false
		}
	}
	cases := map[string]struct {
		e EnqueueDownloadAudio
		m UpsertedMeeting
	}{
		"no recording": {EnqueueDownloadAudio{Enabled: never(t)}, UpsertedMeeting{Meeting: held}},
		"not held": {EnqueueDownloadAudio{Enabled: never(t)}, UpsertedMeeting{
			Meeting: store.Meeting{ID: uuid.New(), Status: "scheduled"}, RecordingURL: "https://x.granicus.com/MediaPlayer.php?clip_id=1",
		}},
		"flag off": {EnqueueDownloadAudio{Enabled: audioFlag(false)}, UpsertedMeeting{Meeting: held, RecordingURL: "https://x.granicus.com/MediaPlayer.php?clip_id=1"}},
		"no flag":  {EnqueueDownloadAudio{}, UpsertedMeeting{Meeting: held, RecordingURL: "https://x.granicus.com/MediaPlayer.php?clip_id=1"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tc.e.MeetingUpserted(t.Context(), nil, tc.m); err != nil {
				t.Errorf("MeetingUpserted = %v", err)
			}
		})
	}
}

type countingFollowUp struct {
	calls int
	err   error
}

func (c *countingFollowUp) MeetingUpserted(context.Context, pgx.Tx, UpsertedMeeting) error {
	c.calls++
	return c.err
}

func TestFollowUpsRunInOrderAndStopAtTheFirstError(t *testing.T) {
	boom := errors.New("boom")
	first, second, third := &countingFollowUp{}, &countingFollowUp{err: boom}, &countingFollowUp{}
	err := FollowUps{first, second, third}.MeetingUpserted(t.Context(), nil, UpsertedMeeting{})
	if !errors.Is(err, boom) || first.calls != 1 || second.calls != 1 || third.calls != 0 {
		t.Errorf("FollowUps = %v, calls %d %d %d", err, first.calls, second.calls, third.calls)
	}
	if err := (FollowUps{first}).MeetingUpserted(t.Context(), nil, UpsertedMeeting{}); err != nil {
		t.Errorf("FollowUps = %v", err)
	}
}

func TestNoAudioFollowUpDoesNothing(t *testing.T) {
	if err := (NoAudioFollowUp{}).AudioStored(t.Context(), nil, store.Recording{}); err != nil {
		t.Errorf("AudioStored = %v", err)
	}
}
