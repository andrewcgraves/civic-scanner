//go:build integration

package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
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
		first.Audio != nil || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Errorf("first recording = %+v", first)
	}

	audio := store.RecordingAudio{Key: "audio/" + first.ID.String() + ".mp3", ExpiresAt: time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)}
	if _, err := s.SetRecordingAudio(ctx, first.ID, audio); err != nil {
		t.Fatal(err)
	}

	again, created, err := s.UpsertRecording(ctx, recordingInput(meetingID, "full"))
	if err != nil || created {
		t.Fatalf("second upsert = (created %v, err %v), want not created", created, err)
	}
	if again.ID != first.ID || again.Audio == nil || again.Audio.Key != audio.Key || !again.Audio.ExpiresAt.Equal(audio.ExpiresAt) {
		t.Errorf("second upsert = %+v, want the same recording with its audio", again)
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

func TestSetAndClearRecordingAudio(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	r := seedRecording(t, pool, "store-test-recording-audio")

	audio := store.RecordingAudio{Key: "audio/" + r.ID.String() + ".mp3", ExpiresAt: time.Date(2027, 5, 1, 12, 0, 0, 0, time.UTC)}
	set, err := s.SetRecordingAudio(ctx, r.ID, audio)
	if err != nil {
		t.Fatalf("SetRecordingAudio: %v", err)
	}
	if set.Audio == nil || set.Audio.Key != audio.Key || !set.Audio.ExpiresAt.Equal(audio.ExpiresAt) {
		t.Errorf("after set: audio = %+v, want %+v", set.Audio, audio)
	}
	if !set.UpdatedAt.After(r.UpdatedAt) || !set.CreatedAt.Equal(r.CreatedAt) || set.SourceURL != r.SourceURL {
		t.Errorf("after set: %+v, from %+v", set, r)
	}

	got, err := s.GetRecording(ctx, r.ID)
	if err != nil || got.Audio == nil || got.Audio.Key != audio.Key {
		t.Errorf("GetRecording after set = (%+v, %v)", got, err)
	}

	cleared, err := s.ClearRecordingAudio(ctx, r.ID)
	if err != nil {
		t.Fatalf("ClearRecordingAudio: %v", err)
	}
	if cleared.Audio != nil || !cleared.UpdatedAt.After(set.UpdatedAt) {
		t.Errorf("after clear: %+v", cleared)
	}
}

func TestRecordingAudioOnMissingRecordingIsNotFound(t *testing.T) {
	ctx := t.Context()
	s := store.New(testdb.Pool(t))
	audio := store.RecordingAudio{Key: "k", ExpiresAt: time.Now()}

	if _, err := s.SetRecordingAudio(ctx, uuid.New(), audio); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetRecordingAudio err = %v, want ErrNotFound", err)
	}
	if _, err := s.ClearRecordingAudio(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ClearRecordingAudio err = %v, want ErrNotFound", err)
	}
}

func TestListRecordingsWithExpiredAudio(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-recording-expired")
	now := time.Date(2091, 6, 1, 0, 0, 0, 0, time.UTC)

	add := func(name string, expiresAt *time.Time) uuid.UUID {
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
		return r.ID
	}
	at := func(d time.Duration) *time.Time {
		v := now.Add(d)
		return &v
	}
	older := add("older", at(-48*time.Hour))
	exact := add("exact", at(0))
	add("future", at(time.Hour))
	add("no-audio", nil)
	cleared := add("cleared", at(-72*time.Hour))
	if _, err := s.ClearRecordingAudio(ctx, cleared); err != nil {
		t.Fatal(err)
	}

	ids := func(recordings []store.Recording) []uuid.UUID {
		var out []uuid.UUID
		for _, r := range recordings {
			if r.MeetingID == meetingID {
				out = append(out, r.ID)
			}
		}
		return out
	}

	expired, err := s.ListRecordingsWithExpiredAudio(ctx, now, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(expired), []uuid.UUID{older, exact}; !slices.Equal(got, want) {
		t.Errorf("expired = %v, want %v", got, want)
	}

	if _, err := pool.Exec(ctx, `UPDATE recordings SET audio_expires_at = audio_expires_at - interval '100 years' WHERE meeting_id = $1 AND audio_key IS NOT NULL`, meetingID); err != nil {
		t.Fatal(err)
	}
	first, err := s.ListRecordingsWithExpiredAudio(ctx, time.Date(1991, 6, 1, 0, 0, 0, 0, time.UTC), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(first); len(first) != 1 || !slices.Equal(got, []uuid.UUID{older}) {
		t.Errorf("limit 1 = %v, want only %v", got, older)
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
	_, failures["expired"] = s.ListRecordingsWithExpiredAudio(ctx, time.Now(), 10)
	_, failures["set audio"] = s.SetRecordingAudio(ctx, uuid.New(), audio)
	_, failures["clear audio"] = s.ClearRecordingAudio(ctx, uuid.New())
	for name, err := range failures {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want a connection error", name, err)
		}
	}
}
