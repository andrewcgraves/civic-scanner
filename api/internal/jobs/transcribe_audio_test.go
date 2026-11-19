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

	"github.com/andrewcgraves/civic-scanner/api/internal/media"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage/storagetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe/transcribetest"
)

func transcribeFlag(on bool) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool { return on && name == FlagTranscribe }
}

func transcribeJob() *river.Job[TranscribeAudioArgs] {
	return &river.Job[TranscribeAudioArgs]{
		JobRow: &rivertype.JobRow{ID: 7, Attempt: 1, MaxAttempts: transcribeMaxAttempts},
		Args:   TranscribeAudioArgs{RecordingID: uuid.New()},
	}
}

func TestTranscribeAudioArgs(t *testing.T) {
	args := TranscribeAudioArgs{}
	if kind := args.Kind(); kind != "transcribe_audio" {
		t.Errorf("Kind() = %q", kind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 3 || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || opts.UniqueOpts.ByPeriod != 0 {
		t.Errorf("InsertOpts() = %+v", opts)
	}
	for _, s := range opts.UniqueOpts.ByState {
		if s == rivertype.JobStateCompleted || s == rivertype.JobStateCancelled || s == rivertype.JobStateDiscarded {
			t.Errorf("unique over final state %s: a failed job would block the sweep", s)
		}
	}
	w := &TranscribeAudioWorker{settings: Transcription{}.withDefaults()}
	if got := w.Timeout(nil); got != 2*time.Hour {
		t.Errorf("Timeout() = %s, want 2h", got)
	}
}

func TestTranscriptionDefaults(t *testing.T) {
	got := Transcription{}.withDefaults()
	if got.Timeout != 2*time.Hour || got.MaxDuration != 8*time.Hour || got.MaxAudioBytes != 128<<20 || got.Concurrency != 1 {
		t.Errorf("defaults = %+v", got)
	}
	if _, ok := got.FollowUp.(NoTranscriptFollowUp); !ok {
		t.Errorf("FollowUp = %#v", got.FollowUp)
	}
	if err := (NoTranscriptFollowUp{}).TranscriptSaved(t.Context(), nil, store.Transcript{}); err != nil {
		t.Errorf("NoTranscriptFollowUp: %v", err)
	}
}

func TestTranscribeAudioCompletesQuietlyWhenItCannotRun(t *testing.T) {
	cases := map[string]*TranscribeAudioWorker{
		"flag off":  {logger: discard, enabled: transcribeFlag(false), settings: Transcription{Client: &transcribetest.Fake{}}},
		"no client": {logger: discard, enabled: transcribeFlag(true)},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			if err := w.Work(t.Context(), transcribeJob()); err != nil {
				t.Errorf("Work = %v, want nil", err)
			}
		})
	}
}

func TestTranscribeFailureKinds(t *testing.T) {
	cases := map[string]error{
		TranscribeKindLimit:        fmt.Errorf("x: %w", transcribe.ErrTooLong),
		TranscribeKindTooLarge:     ErrTranscriptTooLarge,
		TranscribeKindMissingAudio: ErrAudioMissing,
		TranscribeKindUnavailable:  fmt.Errorf("extract audio chunk: %w", media.ErrUnavailable),
		TranscribeKindMalformed:    fmt.Errorf("extract audio chunk: %w", media.ErrMalformed),
		TranscribeKindTransient:    &transcribe.Error{Retryable: true, Err: errors.New("503")},
		TranscribeKindRefused:      &transcribe.Error{StatusCode: 401, Err: errors.New("unauthorized")},
		TranscribeKindFailed:       errors.New("something else"),
	}
	for want, err := range cases {
		if got := TranscribeFailureKind(err); got != want {
			t.Errorf("TranscribeFailureKind(%v) = %s, want %s", err, got, want)
		}
	}
	for _, err := range []error{transcribe.ErrChunkTooLarge, ErrAudioTooLarge, media.ErrLimitExceeded} {
		if got := TranscribeFailureKind(err); got != TranscribeKindLimit {
			t.Errorf("TranscribeFailureKind(%v) = %s, want limit", err, got)
		}
	}
	if !transcribeRetryable(context.DeadlineExceeded) || !transcribeRetryable(errAudioReadShort) || transcribeRetryable(ErrAudioMissing) {
		t.Error("retry classification is wrong")
	}
}

func TestFailedRetriesTransientErrorsAndCancelsTheRest(t *testing.T) {
	w := &TranscribeAudioWorker{logger: discard}
	retry := w.failed(t.Context(), discard, transcribeJob(), &transcribe.Error{Retryable: true, Err: errors.New("busy")})
	var cancel *river.JobCancelError
	if retry == nil || errors.As(retry, &cancel) || !strings.Contains(retry.Error(), "transient") {
		t.Errorf("retryable failure = %v", retry)
	}
	permanent := w.failed(t.Context(), discard, transcribeJob(), fmt.Errorf("x: %w", transcribe.ErrTooLong))
	if !errors.As(permanent, &cancel) || !strings.Contains(permanent.Error(), "limit") {
		t.Errorf("permanent failure = %v", permanent)
	}
}

func TestTranscriptInputCleansAndBoundsModelOutput(t *testing.T) {
	ms := time.Millisecond
	got, err := TranscriptInput(uuid.Nil, transcribe.Transcript{
		Text: "ignored when there are segments",
		Segments: []transcribe.Segment{
			{Start: -5 * ms, End: 100 * ms, Text: " Call‮ to\x00 order.\n\nRoll​ call ", Speaker: "c0/speaker_0⁦"},
			{Start: 200 * ms, End: 150 * ms, Text: "\U000E0041hidden tag\U000E007F"},
			{Start: 300 * ms, End: 400 * ms, Text: " \t‍ "},
			{Start: 500 * ms, End: 600 * ms, Text: strings.Repeat("é", MaxSegmentBytes), Speaker: strings.Repeat("s", 200)},
		},
	})
	if err != nil {
		t.Fatalf("TranscriptInput: %v", err)
	}
	if len(got.Segments) != 3 {
		t.Fatalf("segments = %+v, want the blank one dropped", got.Segments)
	}
	first := got.Segments[0]
	if first.Start != 0 || first.End != 100*ms || first.Text != "Call to order. Roll call" || first.Speaker != "c0/speaker_0" {
		t.Errorf("first segment = %+v", first)
	}
	if second := got.Segments[1]; second.Start != 200*ms || second.End != 200*ms || second.Text != "hidden tag" {
		t.Errorf("second segment = %+v", second)
	}
	long := got.Segments[2]
	if len(long.Text) > MaxSegmentBytes || !strings.HasSuffix(long.Text, "…") || !strings.HasPrefix(long.Text, "é") || len(long.Speaker) > MaxSpeakerBytes {
		t.Errorf("long segment = %d bytes text, %d bytes speaker", len(long.Text), len(long.Speaker))
	}
	if !strings.HasPrefix(got.Text, "Call to order. Roll call hidden tag é") {
		t.Errorf("text = %q", got.Text[:40])
	}
}

func TestTranscriptInputWithoutSegmentsKeepsTheCleanedText(t *testing.T) {
	got, err := TranscriptInput(uuid.Nil, transcribe.Transcript{Text: "  Silence.⁧\x07 "})
	if err != nil || got.Text != "Silence." || len(got.Segments) != 0 {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestTranscriptInputRefusesTranscriptsOverTheLimits(t *testing.T) {
	many := make([]transcribe.Segment, MaxTranscriptSegments+1)
	for i := range many {
		many[i] = transcribe.Segment{Text: "a"}
	}
	if _, err := TranscriptInput(uuid.Nil, transcribe.Transcript{Segments: many}); !errors.Is(err, ErrTranscriptTooLarge) {
		t.Errorf("too many segments: %v", err)
	}
	big := make([]transcribe.Segment, MaxTranscriptBytes/MaxSegmentBytes+2)
	for i := range big {
		big[i] = transcribe.Segment{Text: strings.Repeat("b", MaxSegmentBytes)}
	}
	if _, err := TranscriptInput(uuid.Nil, transcribe.Transcript{Segments: big}); !errors.Is(err, ErrTranscriptTooLarge) {
		t.Errorf("too many bytes: %v", err)
	}
	if _, err := TranscriptInput(uuid.Nil, transcribe.Transcript{Text: strings.Repeat("c", MaxTranscriptBytes+1)}); !errors.Is(err, ErrTranscriptTooLarge) {
		t.Errorf("too long a text: %v", err)
	}
}

func TestChunkCheckpointRoundTripsAndClears(t *testing.T) {
	bucket := storagetest.New()
	info := storage.Info{Key: "recordings/x.mp3", Size: 10, LastModified: time.Unix(100, 5)}
	id := uuid.New()
	prefix := ChunkCheckpointPrefix(id, info, "gateway/elevenlabs/scribe-v2")
	if !strings.HasPrefix(prefix, "transcripts/"+id.String()+"/chunks/") || !strings.HasSuffix(prefix, "/") {
		t.Errorf("prefix = %s", prefix)
	}
	if other := ChunkCheckpointPrefix(id, info, "gateway/microsoft/mai-transcribe-2"); other == prefix {
		t.Error("another model shares the checkpoints")
	}
	info.Size = 11
	if other := ChunkCheckpointPrefix(id, info, "gateway/elevenlabs/scribe-v2"); other == prefix {
		t.Error("other audio shares the checkpoints")
	}
	cp := newChunkCheckpoint(bucket, discard, prefix)
	chunk := transcribe.Chunk{Index: 2, Start: 19*time.Minute + 50*time.Second, Length: 10 * time.Minute}
	if _, ok := cp.Load(t.Context(), chunk); ok {
		t.Fatal("Load before Save hit")
	}
	want := transcribe.Transcript{Text: "hi", Duration: 3 * time.Second, Segments: []transcribe.Segment{{Start: 1500 * time.Microsecond, End: time.Second, Text: "hi", Speaker: "s"}}}
	cp.Save(t.Context(), chunk, want)

	again := newChunkCheckpoint(bucket, discard, prefix)
	got, ok := again.Load(t.Context(), chunk)
	if !ok || got.Text != want.Text || got.Duration != want.Duration || len(got.Segments) != 1 || got.Segments[0] != want.Segments[0] {
		t.Fatalf("Load = %+v, %v", got, ok)
	}
	if again.reused() != 1 {
		t.Errorf("reused = %d", again.reused())
	}
	again.clear(t.Context())
	if keys, _ := bucket.ListOlderThan(t.Context(), time.Now().Add(time.Hour)); len(keys) != 0 {
		t.Errorf("keys left after clear: %v", keys)
	}
}

func TestChunkCheckpointTreatsBadOrUnreachableObjectsAsMisses(t *testing.T) {
	bucket := storagetest.New()
	cp := newChunkCheckpoint(bucket, discard, "transcripts/x/chunks/y/")
	chunk := transcribe.Chunk{Index: 0, Length: time.Minute}
	for _, body := range []string{"not json", `{"segments":[{"start_ns":5,"end_ns":1,"text":"x"}]}`, strings.Repeat(" ", chunkCheckpointMaxBytes+1)} {
		if err := bucket.Put(t.Context(), cp.key(chunk), strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		if _, ok := cp.Load(t.Context(), chunk); ok {
			t.Errorf("Load of %.20q hit", body)
		}
	}
	failing := newChunkCheckpoint(storagetest.Failing(errors.New("down")), discard, "p/")
	if _, ok := failing.Load(t.Context(), chunk); ok {
		t.Error("Load from a failing bucket hit")
	}
	failing.Save(t.Context(), chunk, transcribe.Transcript{})
	failing.remember("p/k", false)
	failing.clear(t.Context())
}

func TestEnqueueTranscribeAudioNeverFailsTheAudioTransaction(t *testing.T) {
	rec := store.Recording{ID: uuid.New()}
	off := EnqueueTranscribeAudio{Enabled: transcribeFlag(false)}
	if err := off.AudioStored(t.Context(), nil, rec); err != nil {
		t.Errorf("flag off: %v", err)
	}
	if err := (EnqueueTranscribeAudio{}).AudioStored(t.Context(), nil, rec); err != nil {
		t.Errorf("no flag function: %v", err)
	}
	noClient := EnqueueTranscribeAudio{Enabled: transcribeFlag(true)}
	if err := noClient.AudioStored(t.Context(), nil, rec); err != nil {
		t.Errorf("no River client: %v", err)
	}
}

type fakeTranscriptSweepStore struct {
	ids   []uuid.UUID
	err   error
	calls []store.UntranscribedParams
}

func (s *fakeTranscriptSweepStore) ListUntranscribedRecordings(_ context.Context, p store.UntranscribedParams) ([]uuid.UUID, error) {
	s.calls = append(s.calls, p)
	return s.ids, s.err
}

func transcriptSweep(st *fakeTranscriptSweepStore, on bool, client transcribe.Client) *EnqueueTranscriptionsWorker {
	return &EnqueueTranscriptionsWorker{logger: discard, store: st, transcriber: client, enabled: transcribeFlag(on), queue: AudioQueue}
}

func transcriptSweepJob() *river.Job[EnqueueTranscriptionsArgs] {
	return &river.Job[EnqueueTranscriptionsArgs]{JobRow: &rivertype.JobRow{ID: 3}}
}

func TestEnqueueTranscriptionsArgs(t *testing.T) {
	args := EnqueueTranscriptionsArgs{}
	if args.Kind() != "enqueue_transcriptions" || !args.InsertOpts().UniqueOpts.ByArgs {
		t.Errorf("args = %s %+v", args.Kind(), args.InsertOpts())
	}
	gotArgs, gotOpts := periodicTranscriptSweep(sourcestest.NewClock(sweepNow), "ingest")()
	if got, ok := gotArgs.(EnqueueTranscriptionsArgs); !ok || !got.Slot.Equal(time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)) || gotOpts.Queue != "ingest" {
		t.Errorf("periodic constructor = %#v, %+v", gotArgs, gotOpts)
	}
}

func TestTranscriptSweepDoesNothingWhenTranscriptionCannotRun(t *testing.T) {
	for name, w := range map[string]*EnqueueTranscriptionsWorker{
		"flag off":  transcriptSweep(&fakeTranscriptSweepStore{}, false, &transcribetest.Fake{}),
		"no client": transcriptSweep(&fakeTranscriptSweepStore{}, true, nil),
	} {
		t.Run(name, func(t *testing.T) {
			if err := w.Work(t.Context(), transcriptSweepJob()); err != nil {
				t.Fatal(err)
			}
			if calls := w.store.(*fakeTranscriptSweepStore).calls; len(calls) != 0 {
				t.Errorf("store asked %d times", len(calls))
			}
		})
	}
}

func TestTranscriptSweepAsksForABoundedBackedOffBatch(t *testing.T) {
	st := &fakeTranscriptSweepStore{}
	if err := transcriptSweep(st, true, &transcribetest.Fake{}).Work(t.Context(), transcriptSweepJob()); err != nil {
		t.Fatal(err)
	}
	want := store.UntranscribedParams{FailedKind: "transcribe_audio", FailedWithin: 7 * 24 * time.Hour, Limit: 10}
	if len(st.calls) != 1 || st.calls[0] != want {
		t.Errorf("calls = %+v, want %+v", st.calls, want)
	}
}

func TestTranscriptSweepRetriesStoreErrorsAndNeedsARiverClient(t *testing.T) {
	boom := errors.New("db down")
	if err := transcriptSweep(&fakeTranscriptSweepStore{err: boom}, true, &transcribetest.Fake{}).Work(t.Context(), transcriptSweepJob()); !errors.Is(err, boom) {
		t.Errorf("store error = %v", err)
	}
	if err := transcriptSweep(&fakeTranscriptSweepStore{ids: []uuid.UUID{uuid.New()}}, true, &transcribetest.Fake{}).Work(t.Context(), transcriptSweepJob()); err == nil {
		t.Error("no River client: want an error")
	}
}
