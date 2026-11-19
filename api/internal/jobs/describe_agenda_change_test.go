package jobs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/agendadiff"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/summarize"
)

const (
	changeBefore = "REGULAR BOARD MEETING\n1. Call to Order\n2. Approve a contract for $1,250,000"
	changeAfter  = "REGULAR BOARD MEETING\n1. Call to Order\n2. Approve a contract for $1,500,000"
)

var changeNow = time.Date(2026, 11, 18, 10, 30, 0, 0, time.UTC)

type changeRecord struct {
	id     uuid.UUID
	note   string
	source store.NoteSource
}

type fakeChangeStore struct {
	mu           sync.Mutex
	versions     map[uuid.UUID]store.AgendaVersion
	getErr       error
	previousErr  error
	earlierErr   error
	failedErr    error
	failedBefore bool
	recordErr    error
	records      []changeRecord
}

func newFakeChangeStore(versions ...store.AgendaVersion) *fakeChangeStore {
	s := &fakeChangeStore{versions: map[uuid.UUID]store.AgendaVersion{}}
	for _, v := range versions {
		s.versions[v.ID] = v
	}
	return s
}

func (s *fakeChangeStore) GetAgendaVersion(_ context.Context, id uuid.UUID) (store.AgendaVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return store.AgendaVersion{}, s.getErr
	}
	v, ok := s.versions[id]
	if !ok {
		return store.AgendaVersion{}, store.ErrNotFound
	}
	return v, nil
}

func (s *fakeChangeStore) PreviousAgendaVersion(_ context.Context, v store.AgendaVersion) (store.AgendaVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.previousErr != nil {
		return store.AgendaVersion{}, s.previousErr
	}
	for _, other := range s.versions {
		if other.MeetingID == v.MeetingID && other.Version == v.Version-1 {
			return other, nil
		}
	}
	return store.AgendaVersion{}, store.ErrNotFound
}

func (s *fakeChangeStore) earlierPending(v store.AgendaVersion) []store.AgendaVersion {
	var earlier []store.AgendaVersion
	for _, other := range s.versions {
		if other.MeetingID == v.MeetingID && other.Version < v.Version && other.ChangeEventPending {
			earlier = append(earlier, other)
		}
	}
	slices.SortFunc(earlier, func(a, b store.AgendaVersion) int { return a.Version - b.Version })
	return earlier
}

func (s *fakeChangeStore) EarlierPendingAgendaVersions(_ context.Context, v store.AgendaVersion) ([]store.AgendaVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.earlierErr != nil {
		return nil, s.earlierErr
	}
	return s.earlierPending(v), nil
}

func (s *fakeChangeStore) AgendaJobFailedBefore(context.Context, string, uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failedBefore, s.failedErr
}

func (s *fakeChangeStore) RecordAgendaChange(_ context.Context, id uuid.UUID, note string, source store.NoteSource) (store.AgendaVersion, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return store.AgendaVersion{}, false, s.recordErr
	}
	v := s.versions[id]
	if len(s.earlierPending(v)) > 0 {
		return store.AgendaVersion{}, false, store.ErrEarlierChangePending
	}
	if !v.ChangeEventPending {
		return v, false, nil
	}
	v.ChangeEventPending = false
	v.ChangeNote = &note
	s.versions[id] = v
	s.records = append(s.records, changeRecord{id: id, note: note, source: source})
	return v, true, nil
}

func (s *fakeChangeStore) recorded() []changeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]changeRecord(nil), s.records...)
}

func changePair() (store.AgendaVersion, store.AgendaVersion) {
	meetingID := uuid.New()
	v1 := agendaVersion(meetingID, 1, changeBefore)
	v2 := agendaVersion(meetingID, 2, changeAfter)
	v2.ChangeEventPending = true
	v2.CreatedAt = changeNow
	return v1, v2
}

func changeFlag(on bool) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool { return on && name == FlagAgendaChanges }
}

func changeWorker(st changeStore, client llm.Client) *DescribeAgendaChangeWorker {
	return &DescribeAgendaChangeWorker{
		logger: discard, store: st, clock: sourcestest.NewClock(changeNow), llm: client, tier: llm.TierRoutine, enabled: changeFlag(true),
	}
}

func changeJob(id uuid.UUID, attempt int) *river.Job[DescribeAgendaChangeArgs] {
	return &river.Job[DescribeAgendaChangeArgs]{
		JobRow: &rivertype.JobRow{ID: 9, Attempt: attempt, MaxAttempts: checkMaxAttempts},
		Args:   DescribeAgendaChangeArgs{AgendaVersionID: id},
	}
}

func isSnooze(err error) bool {
	var snooze *rivertype.JobSnoozeError
	return errors.As(err, &snooze)
}

func TestDescribeAgendaChangeArgs(t *testing.T) {
	args := DescribeAgendaChangeArgs{}
	if kind := args.Kind(); kind != "describe_agenda_change" {
		t.Errorf("Kind() = %q", kind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 5 || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || len(opts.UniqueOpts.ByState) != 5 {
		t.Errorf("InsertOpts() = %+v", opts)
	}
	for _, state := range opts.UniqueOpts.ByState {
		if state == rivertype.JobStateCompleted || state == rivertype.JobStateCancelled || state == rivertype.JobStateDiscarded {
			t.Errorf("unique states include final state %q", state)
		}
	}
	if kind := (EnqueueAgendaChangesArgs{}).Kind(); kind != "enqueue_agenda_changes" {
		t.Errorf("sweep Kind() = %q", kind)
	}
	if opts := (EnqueueAgendaChangesArgs{}).InsertOpts(); !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Errorf("sweep InsertOpts() = %+v", opts)
	}
	w := &DescribeAgendaChangeWorker{llmTimeout: 2 * time.Minute}
	if got := w.Timeout(changeJob(uuid.New(), 1)); got != 3*time.Minute {
		t.Errorf("Timeout = %v, want 3m", got)
	}
}

func TestTheModelDescribesARealChange(t *testing.T) {
	v1, v2 := changePair()
	st := newFakeChangeStore(v1, v2)
	fake := llmtest.Returning("The contract rose from $1,250,000 to $1,500,000.")

	if err := changeWorker(st, fake).Work(t.Context(), changeJob(v2.ID, 1)); err != nil {
		t.Fatal(err)
	}
	want := []changeRecord{{id: v2.ID, note: "The contract rose from $1,250,000 to $1,500,000.", source: store.NoteFromModel}}
	if got := st.recorded(); !slices.Equal(got, want) {
		t.Errorf("recorded = %+v", got)
	}
	if calls := fake.Calls(); len(calls) != 1 || !strings.Contains(calls[0].User, "Removed: $1,250,000\nAdded: $1,500,000") {
		t.Errorf("calls = %+v", calls)
	}
}

func TestTheWordDiffDescribesTheChangeWithoutTheModel(t *testing.T) {
	diff, err := agendadiff.Compare(t.Context(), changeBefore, changeAfter)
	if err != nil {
		t.Fatal(err)
	}
	want := summarize.FallbackChangeNote(diff)
	cases := map[string]struct {
		client       llm.Client
		enabled      func(context.Context, string) bool
		attempt      int
		failedBefore bool
		mayCall      bool
	}{
		"flag off":                 {llmtest.Returning("never"), changeFlag(false), 1, false, false},
		"no LLM":                   {nil, changeFlag(true), 1, false, false},
		"a retry":                  {llmtest.Returning("never"), changeFlag(true), 2, false, false},
		"an earlier job failed":    {llmtest.Returning("never"), changeFlag(true), 1, true, false},
		"LLM not configured":       {llmtest.Failing(llm.ErrNotConfigured), changeFlag(true), 1, false, true},
		"permanent model error":    {llmtest.Failing(&llm.Error{StatusCode: 400, Err: errors.New("bad")}), changeFlag(true), 1, false, true},
		"truncated reply":          {llmtest.Failing(llm.ErrTruncated), changeFlag(true), 1, false, true},
		"retryable model error":    {llmtest.Failing(&llm.Error{Retryable: true, Err: errors.New("busy")}), changeFlag(true), 1, false, true},
		"reply empty when cleaned": {llmtest.Returning("\u200b"), changeFlag(true), 1, false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v1, v2 := changePair()
			st := newFakeChangeStore(v1, v2)
			st.failedBefore = tc.failedBefore
			w := changeWorker(st, tc.client)
			w.enabled = tc.enabled
			if err := w.Work(t.Context(), changeJob(v2.ID, tc.attempt)); err != nil {
				t.Fatal(err)
			}
			if got := st.recorded(); len(got) != 1 || got[0].note != want || got[0].source != store.NoteFromDiff {
				t.Errorf("recorded = %+v, want %q from the diff", got, want)
			}
			if fake, ok := tc.client.(*llmtest.Fake); ok && !tc.mayCall && len(fake.Calls()) != 0 {
				t.Errorf("model called %d times", len(fake.Calls()))
			}
		})
	}
}

func TestDescribingSkipsWhatIsAlreadyRecordedOrHasNoDifferences(t *testing.T) {
	v1, v2 := changePair()
	v2.ChangeEventPending = false
	st := newFakeChangeStore(v1, v2)
	fake := llmtest.Returning("never")
	if err := changeWorker(st, fake).Work(t.Context(), changeJob(v2.ID, 1)); err != nil || len(st.recorded()) != 0 || len(fake.Calls()) != 0 {
		t.Errorf("already recorded: err %v, records %d, calls %d", err, len(st.recorded()), len(fake.Calls()))
	}

	meetingID := uuid.New()
	same1 := agendaVersion(meetingID, 1, changeBefore)
	same2 := agendaVersion(meetingID, 2, "# "+changeBefore+"\nPage 1 of 1")
	same2.ChangeEventPending = true
	st = newFakeChangeStore(same1, same2)
	if err := changeWorker(st, fake).Work(t.Context(), changeJob(same2.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if got := st.recorded(); len(got) != 1 || got[0].note != summarize.RevisedNote || len(fake.Calls()) != 0 {
		t.Errorf("no differences: recorded %+v, calls %d", got, len(fake.Calls()))
	}

	big := strings.Repeat("a ", agendadiff.MaxCompareBytes)
	huge1, huge2 := agendaVersion(meetingID, 1, big), agendaVersion(meetingID, 2, big+"b")
	huge2.ChangeEventPending = true
	st = newFakeChangeStore(huge1, huge2)
	if err := changeWorker(st, fake).Work(t.Context(), changeJob(huge2.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if got := st.recorded(); len(got) != 1 || got[0].note != summarize.RevisedNote || len(fake.Calls()) != 0 {
		t.Errorf("too large: recorded %+v, calls %d", got, len(fake.Calls()))
	}

	lone := agendaVersion(uuid.New(), 2, changeAfter)
	lone.ChangeEventPending = true
	st = newFakeChangeStore(lone)
	if err := changeWorker(st, nil).Work(t.Context(), changeJob(lone.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if got := st.recorded(); len(got) != 1 || !strings.HasPrefix(got[0].note, "Added: ") {
		t.Errorf("without a previous version: recorded %+v", got)
	}
}

func TestAChangeWaitsForAnEarlierPendingVersion(t *testing.T) {
	v1, v2 := changePair()
	v3 := agendaVersion(v1.MeetingID, 3, changeAfter+"\n3. Adjournment")
	v3.ChangeEventPending = true
	v2.CreatedAt = changeNow.Add(-10 * time.Minute)
	st := newFakeChangeStore(v1, v2, v3)
	fake := llmtest.Returning("note")

	err := changeWorker(st, fake).Work(t.Context(), changeJob(v3.ID, 1))
	if !isSnooze(err) || len(st.recorded()) != 0 || len(fake.Calls()) != 0 {
		t.Errorf("err = %v, records %d, calls %d: want a snooze before any model call", err, len(st.recorded()), len(fake.Calls()))
	}

	v2.CreatedAt = changeNow.Add(-changeOrderWait - time.Minute)
	st = newFakeChangeStore(v1, v2, v3)
	if err := changeWorker(st, fake).Work(t.Context(), changeJob(v3.ID, 1)); err != nil {
		t.Fatal(err)
	}
	got := st.recorded()
	if len(got) != 2 || got[0].id != v2.ID || got[0].source != store.NoteFromDiff || got[1].id != v3.ID || got[1].source != store.NoteFromModel {
		t.Errorf("recorded = %+v, want version 2 from the diff, then version 3", got)
	}
}

func TestDescribeAgendaChangeStoreErrors(t *testing.T) {
	boom := errors.New("database down")
	cases := map[string]struct {
		configure func(*fakeChangeStore)
		missing   bool
		check     func(error) bool
	}{
		"missing version":     {func(*fakeChangeStore) {}, true, isCancel},
		"get fails":           {func(s *fakeChangeStore) { s.getErr = boom }, false, nil},
		"earlier fails":       {func(s *fakeChangeStore) { s.earlierErr = boom }, false, nil},
		"previous fails":      {func(s *fakeChangeStore) { s.previousErr = boom }, false, nil},
		"failed-before fails": {func(s *fakeChangeStore) { s.failedErr = boom }, false, nil},
		"record fails":        {func(s *fakeChangeStore) { s.recordErr = boom }, false, nil},
		"record finds none":   {func(s *fakeChangeStore) { s.recordErr = store.ErrNotFound }, false, isCancel},
		"record finds an earlier pending version": {func(s *fakeChangeStore) { s.recordErr = store.ErrEarlierChangePending }, false, isSnooze},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v1, v2 := changePair()
			st := newFakeChangeStore(v1, v2)
			tc.configure(st)
			id := v2.ID
			if tc.missing {
				id = uuid.New()
			}
			err := changeWorker(st, llmtest.Returning("note")).Work(t.Context(), changeJob(id, 1))
			switch {
			case err == nil:
				t.Error("err = nil")
			case tc.check != nil && !tc.check(err):
				t.Errorf("err = %v, wrong kind", err)
			case tc.check == nil && (isCancel(err) || isSnooze(err)):
				t.Errorf("err = %v, want a retry", err)
			}
		})
	}
}

func TestACancelledContextIsRetried(t *testing.T) {
	v1, v2 := changePair()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := changeWorker(newFakeChangeStore(v1, v2), nil).Work(ctx, changeJob(v2.ID, 1))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the context error", err)
	}
}

type fakeChangeSweepStore struct {
	pending []store.PendingAgendaChange
	err     error
	before  time.Time
	limit   int
}

func (s *fakeChangeSweepStore) ListPendingAgendaChanges(_ context.Context, before time.Time, limit int) ([]store.PendingAgendaChange, error) {
	s.before, s.limit = before, limit
	return s.pending, s.err
}

func TestTheChangeSweepAsksForStalePendingVersions(t *testing.T) {
	st := &fakeChangeSweepStore{}
	w := &EnqueueAgendaChangesWorker{logger: discard, store: st, clock: sourcestest.NewClock(changeNow)}
	job := &river.Job[EnqueueAgendaChangesArgs]{JobRow: &rivertype.JobRow{ID: 3}}

	if err := w.Work(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if !st.before.Equal(changeNow.Add(-changeSweepGrace)) || st.limit != changeSweepLimit {
		t.Errorf("asked for versions before %v, limit %d", st.before, st.limit)
	}

	st.err = errors.New("database down")
	if err := w.Work(t.Context(), job); !errors.Is(err, st.err) {
		t.Errorf("err = %v, want the store error", err)
	}

	st.err = nil
	st.pending = []store.PendingAgendaChange{{ID: uuid.New(), CreatedAt: changeNow.Add(-changeStaleAfter - time.Hour)}}
	if err := w.Work(t.Context(), job); err == nil {
		t.Error("err = nil without a River client in the context")
	}

	args, opts := periodicChangeSweep(sourcestest.NewClock(changeNow), "q")()
	if args.(EnqueueAgendaChangesArgs).Slot != time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC) || opts.Queue != "q" {
		t.Errorf("periodic args = %+v, opts = %+v", args, opts)
	}
}

func TestEnqueueDescribeAgendaChangeNeedsARiverClient(t *testing.T) {
	_, v2 := changePair()
	if err := enqueueDescribeAgendaChange(t.Context(), nil, "q", v2); err == nil || !strings.Contains(err.Error(), "enqueue describe agenda change") {
		t.Errorf("err = %v", err)
	}
}
