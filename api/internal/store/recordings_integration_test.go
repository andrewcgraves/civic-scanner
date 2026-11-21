//go:build integration

package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func recordingInput(meetingID uuid.UUID, name string) store.RecordingInput {
	return store.RecordingInput{
		MeetingID: meetingID,
		SourceURL: "https://example.com/recordings/" + meetingID.String() + "/" + name + ".mp4",
	}
}

func seedRecording(t *testing.T, pool *pgxpool.Pool, slug string) store.Recording {
	t.Helper()
	meetingID := seedMeeting(t, pool, slug)
	r, _, err := store.New(pool).UpsertRecording(t.Context(), recordingInput(meetingID, "full"))
	if err != nil {
		t.Fatalf("UpsertRecording: %v", err)
	}
	return r
}

func recordingTupleID(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var ctid string
	if err := pool.QueryRow(t.Context(), `SELECT ctid::text || '/' || xmin::text FROM recordings WHERE id = $1`, id).Scan(&ctid); err != nil {
		t.Fatal(err)
	}
	return ctid
}

func TestUpsertRecordingCreatesOnceAndKeepsAudio(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-recording-upsert")

	first, created, err := s.UpsertRecording(ctx, recordingInput(meetingID, "full"))
	if err != nil || !created {
		t.Fatalf("first upsert = (created %v, err %v), want created", created, err)
	}
	if first.ID == uuid.Nil || first.MeetingID != meetingID || first.SourceURL != recordingInput(meetingID, "full").SourceURL ||
		first.Audio != nil || first.AudioDeletedAt != nil || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Errorf("first recording = %+v", first)
	}

	audio := store.RecordingAudio{Key: "audio/" + first.ID.String() + ".mp3", ExpiresAt: time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)}
	if _, err := s.SetRecordingAudio(ctx, first.ID, audio); err != nil {
		t.Fatal(err)
	}
	tuple := recordingTupleID(t, pool, first.ID)

	again, created, err := s.UpsertRecording(ctx, recordingInput(meetingID, "full"))
	if err != nil || created {
		t.Fatalf("second upsert = (created %v, err %v), want not created", created, err)
	}
	if again.ID != first.ID || again.Audio == nil || again.Audio.Key != audio.Key || !again.Audio.ExpiresAt.Equal(audio.ExpiresAt) {
		t.Errorf("second upsert = %+v, want the same recording with its audio", again)
	}
	if got := recordingTupleID(t, pool, first.ID); got != tuple {
		t.Errorf("repeat upsert wrote a new tuple: %s -> %s", tuple, got)
	}

	other, created, err := s.UpsertRecording(ctx, recordingInput(meetingID, "part-2"))
	if err != nil || !created || other.ID == first.ID {
		t.Errorf("other source url = (%+v, created %v, err %v), want a new recording", other, created, err)
	}
}

func TestUpsertRecordingForMissingMeetingIsNotFound(t *testing.T) {
	s := store.New(testdb.Pool(t))

	if _, _, err := s.UpsertRecording(t.Context(), recordingInput(uuid.New(), "orphan")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUpsertRecordingTxForMissingMeetingLeavesTheTransactionUsable(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-recording-tx-missing")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	if _, _, err := s.UpsertRecordingTx(ctx, tx, recordingInput(uuid.New(), "orphan")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, created, err := s.UpsertRecordingTx(ctx, tx, recordingInput(meetingID, "full")); err != nil || !created {
		t.Fatalf("upsert after ErrNotFound = (created %v, err %v), want created", created, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if recordings, err := s.ListRecordings(ctx, meetingID); err != nil || len(recordings) != 1 {
		t.Errorf("after commit = (%v, %v), want one recording", recordings, err)
	}
}

func TestUpsertRecordingRejectsEmptySourceURL(t *testing.T) {
	pool := testdb.Pool(t)
	meetingID := seedMeeting(t, pool, "store-test-recording-no-url")

	_, _, err := store.New(pool).UpsertRecording(t.Context(), store.RecordingInput{MeetingID: meetingID})
	if err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want a constraint error", err)
	}
}

func TestUpsertRecordingTxRollsBackWithCaller(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-recording-tx")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	r, created, err := s.UpsertRecordingTx(ctx, tx, recordingInput(meetingID, "full"))
	if err != nil || !created {
		t.Fatalf("UpsertRecordingTx = (created %v, err %v), want created", created, err)
	}
	if _, err := s.SetRecordingAudioTx(ctx, tx, r.ID, store.RecordingAudio{Key: "k", ExpiresAt: time.Now()}); err != nil {
		t.Fatalf("SetRecordingAudioTx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetRecording(ctx, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rolled back recording: err = %v, want ErrNotFound", err)
	}
}

func TestGetAndListRecordings(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-recording-list")
	other := seedMeeting(t, pool, "store-test-recording-list-other")

	var want []uuid.UUID
	for _, name := range []string{"part-1", "part-2", "part-3"} {
		r, _, err := s.UpsertRecording(ctx, recordingInput(meetingID, name))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, r.ID)
	}
	if _, _, err := s.UpsertRecording(ctx, recordingInput(other, "part-1")); err != nil {
		t.Fatal(err)
	}

	recordings, err := s.ListRecordings(ctx, meetingID)
	if err != nil {
		t.Fatal(err)
	}
	var got []uuid.UUID
	for _, r := range recordings {
		got = append(got, r.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("recordings = %v, want %v in insert order", got, want)
	}

	one, err := s.GetRecording(ctx, want[1])
	if err != nil || one.ID != want[1] || one.MeetingID != meetingID {
		t.Errorf("GetRecording = (%+v, %v)", one, err)
	}
	if _, err := s.GetRecording(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetRecording unknown id: err = %v, want ErrNotFound", err)
	}

	none, err := s.ListRecordings(ctx, uuid.New())
	if err != nil || len(none) != 0 {
		t.Errorf("unknown meeting = (%v, %v), want empty", none, err)
	}
}

func TestListRecordingsKeepsInsertOrderWithinOneTransaction(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-recording-list-tx")

	var want []uuid.UUID
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for i := range 20 {
			r, _, err := s.UpsertRecordingTx(ctx, tx, recordingInput(meetingID, fmt.Sprintf("part-%02d", i)))
			if err != nil {
				return err
			}
			want = append(want, r.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	recordings, err := s.ListRecordings(ctx, meetingID)
	if err != nil {
		t.Fatal(err)
	}
	var got []uuid.UUID
	for _, r := range recordings {
		got = append(got, r.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("recordings = %v, want %v in insert order", got, want)
	}
}

func TestSetAudioThenMarkItDeleted(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-recording-audio")

	audio := store.RecordingAudio{Key: "audio/" + r.ID.String() + ".mp3", ExpiresAt: time.Date(2027, 5, 1, 12, 0, 0, 0, time.UTC)}
	set, err := s.SetRecordingAudio(ctx, r.ID, audio)
	if err != nil {
		t.Fatalf("SetRecordingAudio: %v", err)
	}
	if set.Audio == nil || set.Audio.Key != audio.Key || !set.Audio.ExpiresAt.Equal(audio.ExpiresAt) || set.AudioDeletedAt != nil {
		t.Errorf("after set: audio = %+v, deleted at %v, want %+v and not deleted", set.Audio, set.AudioDeletedAt, audio)
	}
	if !set.UpdatedAt.After(r.UpdatedAt) || !set.CreatedAt.Equal(r.CreatedAt) || set.SourceURL != r.SourceURL {
		t.Errorf("after set: %+v, from %+v", set, r)
	}

	got, err := s.GetRecording(ctx, r.ID)
	if err != nil || got.Audio == nil || got.Audio.Key != audio.Key {
		t.Errorf("GetRecording after set = (%+v, %v)", got, err)
	}

	deleted, err := s.MarkRecordingAudioDeleted(ctx, r.ID, audio.Key)
	if err != nil {
		t.Fatalf("MarkRecordingAudioDeleted: %v", err)
	}
	if deleted.Audio != nil || deleted.AudioDeletedAt == nil || !deleted.UpdatedAt.After(set.UpdatedAt) {
		t.Errorf("after delete: %+v", deleted)
	}

	if _, err := s.MarkRecordingAudioDeleted(ctx, r.ID, audio.Key); !errors.Is(err, store.ErrAudioChanged) {
		t.Errorf("second delete: err = %v, want ErrAudioChanged", err)
	}

	again, err := s.SetRecordingAudio(ctx, r.ID, store.RecordingAudio{Key: "audio/again.mp3", ExpiresAt: audio.ExpiresAt})
	if err != nil || again.Audio == nil || again.AudioDeletedAt != nil {
		t.Errorf("audio stored again = (%+v, %v), want audio and no deletion time", again, err)
	}
}

func TestMarkRecordingAudioDeletedKeepsAudioStoredInBetween(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-recording-audio-race")
	expires := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)

	if _, err := s.SetRecordingAudio(ctx, r.ID, store.RecordingAudio{Key: "audio/old.mp3", ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRecordingAudio(ctx, r.ID, store.RecordingAudio{Key: "audio/new.mp3", ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.MarkRecordingAudioDeleted(ctx, r.ID, "audio/old.mp3"); !errors.Is(err, store.ErrAudioChanged) {
		t.Errorf("stale key: err = %v, want ErrAudioChanged", err)
	}
	got, err := s.GetRecording(ctx, r.ID)
	if err != nil || got.Audio == nil || got.Audio.Key != "audio/new.mp3" || got.AudioDeletedAt != nil {
		t.Errorf("after stale delete = (%+v, %v), want the new audio kept", got, err)
	}
}

func TestRecordingAudioOnMissingRecordingIsNotFound(t *testing.T) {
	ctx := t.Context()
	s := store.New(testdb.Pool(t))
	audio := store.RecordingAudio{Key: "k", ExpiresAt: time.Now()}

	if _, err := s.SetRecordingAudio(ctx, uuid.New(), audio); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetRecordingAudio err = %v, want ErrNotFound", err)
	}
	if _, err := s.MarkRecordingAudioDeleted(ctx, uuid.New(), "k"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("MarkRecordingAudioDeleted err = %v, want ErrNotFound", err)
	}
	if _, err := s.AddRecordingAudio(ctx, uuid.New(), audio); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("AddRecordingAudio err = %v, want ErrNotFound", err)
	}
}

func TestRecordDownloadFailureCountsAttemptsAndAudioClearsIt(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-recording-download-failure")
	if r.Download != (store.DownloadState{}) {
		t.Errorf("new recording download state = %+v", r.Download)
	}

	retry := time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)
	first, err := s.RecordDownloadFailure(ctx, r.ID, store.DownloadFailure{Kind: "refused", RetryAfter: &retry})
	if err != nil {
		t.Fatalf("RecordDownloadFailure: %v", err)
	}
	if first.Download.Attempts != 1 || first.Download.Failure != "refused" || first.Download.FailedAt == nil ||
		first.Download.RetryAfter == nil || !first.Download.RetryAfter.Equal(retry) {
		t.Errorf("after first failure = %+v", first.Download)
	}
	second, err := s.RecordDownloadFailure(ctx, r.ID, store.DownloadFailure{Kind: "no_media"})
	if err != nil || second.Download.Attempts != 2 || second.Download.Failure != "no_media" || second.Download.RetryAfter != nil {
		t.Errorf("after second failure = %+v, %v", second.Download, err)
	}

	added, err := s.AddRecordingAudio(ctx, r.ID, store.RecordingAudio{Key: "recordings/x.mp3", ExpiresAt: retry})
	if err != nil {
		t.Fatal(err)
	}
	if added.Download.Failure != "" || added.Download.FailedAt != nil || added.Download.RetryAfter != nil || added.Download.Attempts != 2 {
		t.Errorf("after audio = %+v, want the failure cleared and the count kept", added.Download)
	}

	if _, err := s.RecordDownloadFailure(ctx, r.ID, store.DownloadFailure{Kind: "gone"}); !errors.Is(err, store.ErrInvalidFailure) {
		t.Errorf("unknown kind err = %v, want ErrInvalidFailure", err)
	}
	if _, err := s.RecordDownloadFailure(ctx, uuid.New(), store.DownloadFailure{Kind: "refused"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing recording err = %v, want ErrNotFound", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE recordings SET download_failure = 'gone' WHERE id = $1`, r.ID); err == nil {
		t.Error("the check constraint accepted an unknown failure kind")
	}
}

func TestAddRecordingAudioOnlyWhenThereIsNoneAndNoneWasDeleted(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-recording-add-audio")
	audio := store.RecordingAudio{Key: "recordings/" + r.ID.String() + ".mp3", ExpiresAt: time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)}

	added, err := s.AddRecordingAudio(ctx, r.ID, audio)
	if err != nil || added.Audio == nil || added.Audio.Key != audio.Key || !added.Audio.ExpiresAt.Equal(audio.ExpiresAt) {
		t.Fatalf("AddRecordingAudio = (%+v, %v)", added, err)
	}
	later := store.RecordingAudio{Key: "recordings/other.mp3", ExpiresAt: audio.ExpiresAt.AddDate(1, 0, 0)}
	if _, err := s.AddRecordingAudio(ctx, r.ID, later); !errors.Is(err, store.ErrAudioChanged) {
		t.Errorf("second add err = %v, want ErrAudioChanged", err)
	}
	if got, _ := s.GetRecording(ctx, r.ID); got.Audio == nil || got.Audio.Key != audio.Key {
		t.Errorf("after second add = %+v, want the first audio kept", got.Audio)
	}

	if _, err := s.MarkRecordingAudioDeleted(ctx, r.ID, audio.Key); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := s.AddRecordingAudioTx(ctx, tx, r.ID, audio)
		return err
	})
	if !errors.Is(err, store.ErrAudioChanged) {
		t.Errorf("add after retention deleted the audio: err = %v, want ErrAudioChanged", err)
	}
	if got, _ := s.GetRecording(ctx, r.ID); got.Audio != nil || got.AudioDeletedAt == nil {
		t.Errorf("after add on deleted audio = %+v, want it to stay deleted", got)
	}
}

func TestListTranscribedRecordingsWithExpiredAudio(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-recording-expired")
	now := time.Date(1850, 6, 1, 0, 0, 0, 0, time.UTC)

	add := func(name string, expiresAt *time.Time, transcribed bool) uuid.UUID {
		t.Helper()
		r, _, err := s.UpsertRecording(ctx, recordingInput(meetingID, name))
		if err != nil {
			t.Fatal(err)
		}
		if expiresAt != nil {
			if _, err := s.SetRecordingAudio(ctx, r.ID, store.RecordingAudio{Key: "audio/" + name, ExpiresAt: *expiresAt}); err != nil {
				t.Fatal(err)
			}
		}
		if transcribed {
			if _, _, err := s.SaveTranscript(ctx, transcriptInput(r.ID, "Call to order.")); err != nil {
				t.Fatal(err)
			}
		}
		return r.ID
	}
	at := func(d time.Duration) *time.Time {
		v := now.Add(d)
		return &v
	}
	older := add("older", at(-48*time.Hour), true)
	exact := add("exact", at(0), true)
	add("untranscribed", at(-96*time.Hour), false)
	add("future", at(time.Hour), true)
	add("no-audio", nil, true)
	deleted := add("deleted", at(-72*time.Hour), true)
	if _, err := s.MarkRecordingAudioDeleted(ctx, deleted, "audio/deleted"); err != nil {
		t.Fatal(err)
	}
	mine := map[uuid.UUID]bool{}
	recordings, err := s.ListRecordings(ctx, meetingID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recordings {
		mine[r.ID] = true
	}

	ids := func(recordings []store.Recording) []uuid.UUID {
		var out []uuid.UUID
		for _, r := range recordings {
			if mine[r.ID] {
				out = append(out, r.ID)
			}
		}
		return out
	}

	expired, err := s.ListTranscribedRecordingsWithExpiredAudio(ctx, now, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(expired), []uuid.UUID{older, exact}; !slices.Equal(got, want) {
		t.Errorf("expired = %v, want %v", got, want)
	}
	for _, r := range expired {
		if r.Audio == nil || r.Audio.ExpiresAt.After(now) {
			t.Errorf("listed %s with audio %+v, want audio expiring at or before %v", r.ID, r.Audio, now)
		}
	}

	first, err := s.ListTranscribedRecordingsWithExpiredAudio(ctx, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Audio == nil || first[0].Audio.ExpiresAt.After(now.Add(-48*time.Hour)) ||
		(mine[first[0].ID] && first[0].ID != older) {
		t.Errorf("limit 1 = %+v, want %s or a recording expiring before it", first, older)
	}
}

func TestRecordingReadsAndWritesFailOnClosedPool(t *testing.T) {
	ctx := t.Context()
	pool, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	s := store.New(pool)
	audio := store.RecordingAudio{Key: "k", ExpiresAt: time.Now()}

	failures := map[string]error{}
	_, _, failures["upsert"] = s.UpsertRecording(ctx, recordingInput(uuid.New(), "x"))
	_, failures["get"] = s.GetRecording(ctx, uuid.New())
	_, failures["list"] = s.ListRecordings(ctx, uuid.New())
	_, failures["expired"] = s.ListTranscribedRecordingsWithExpiredAudio(ctx, time.Now(), 10)
	_, failures["set audio"] = s.SetRecordingAudio(ctx, uuid.New(), audio)
	_, failures["mark deleted"] = s.MarkRecordingAudioDeleted(ctx, uuid.New(), "k")
	_, failures["add audio"] = s.AddRecordingAudio(ctx, uuid.New(), audio)
	_, failures["record failure"] = s.RecordDownloadFailure(ctx, uuid.New(), store.DownloadFailure{Kind: "refused"})
	for name, err := range failures {
		if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrAudioChanged) {
			t.Errorf("%s: err = %v, want a connection error", name, err)
		}
	}
}
