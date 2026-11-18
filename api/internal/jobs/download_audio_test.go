package jobs

import (
	"context"
	"errors"
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

func TestDownloadAudioWithoutDependenciesIsCancelled(t *testing.T) {
	cases := map[string]struct {
		w    *DownloadAudioWorker
		want error
	}{
		"no storage":    {&DownloadAudioWorker{logger: discard, enabled: audioFlag(true), downloader: unusedDownloader{t}}, ErrNoStorage},
		"no downloader": {&DownloadAudioWorker{logger: discard, enabled: audioFlag(true), storage: storagetest.New()}, ErrNoDownloader},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tc.w.Work(t.Context(), audioJob()); !isCancel(err) || !errors.Is(err, tc.want) {
				t.Errorf("Work = %v, want a cancelled %v", err, tc.want)
			}
		})
	}
}

func TestMediaAndStorageErrorsRetryOnlyTransientFailures(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		cancel bool
	}{
		{"network", media.ErrNetwork, false},
		{"blocked player", media.ErrBlocked, false},
		{"job timeout", context.DeadlineExceeded, false},
		{"refused", media.ErrRefused, true},
		{"no media", media.ErrNoMedia, true},
		{"too long", media.ErrLimitExceeded, true},
		{"no tools", media.ErrUnavailable, true},
	}
	for _, tc := range cases {
		if err := mediaError("https://x.test/r", tc.err); isCancel(err) != tc.cancel || !errors.Is(err, tc.err) {
			t.Errorf("%s: mediaError = %v, cancel = %v", tc.name, err, tc.cancel)
		}
	}
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
