package jobs

import (
	"context"
	"errors"
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

type fakeChangeStore struct {
	mu          sync.Mutex
	versions    map[uuid.UUID]store.AgendaVersion
	getErr      error
	previousErr error
	recordErr   error
	records     []string
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

func (s *fakeChangeStore) RecordAgendaChange(_ context.Context, id uuid.UUID, note string) (store.AgendaVersion, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return store.AgendaVersion{}, false, s.recordErr
	}
	v := s.versions[id]
	if !v.ChangeEventPending {
		return v, false, nil
	}
	v.ChangeEventPending = false
	v.ChangeNote = &note
	s.versions[id] = v
	s.records = append(s.records, note)
	return v, true, nil
}

func (s *fakeChangeStore) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.records...)
}

func changePair() (store.AgendaVersion, store.AgendaVersion) {
	meetingID := uuid.New()
	v1 := agendaVersion(meetingID, 1, changeBefore)
	v2 := agendaVersion(meetingID, 2, changeAfter)
	v2.ChangeEventPending = true
	return v1, v2
}

func changeFlag(on bool) func(context.Context, string) bool {
	return func(_ context.Context, name string) bool { return on && name == FlagAgendaChanges }
}

func changeWorker(st changeStore, client llm.Client) *DescribeAgendaChangeWorker {
	return &DescribeAgendaChangeWorker{logger: discard, store: st, llm: client, tier: llm.TierRoutine, enabled: changeFlag(true)}
}

func changeJob(id uuid.UUID, attempt int) *river.Job[DescribeAgendaChangeArgs] {
	return &river.Job[DescribeAgendaChangeArgs]{
		JobRow: &rivertype.JobRow{ID: 9, Attempt: attempt, MaxAttempts: checkMaxAttempts},
		Args:   DescribeAgendaChangeArgs{AgendaVersionID: id},
	}
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
	if got := st.recorded(); len(got) != 1 || got[0] != "The contract rose from $1,250,000 to $1,500,000." {
		t.Errorf("recorded = %q", got)
	}
	if calls := fake.Calls(); len(calls) != 1 || !strings.Contains(calls[0].User, "Removed: $1,250,000\nAdded: $1,500,000") {
		t.Errorf("calls = %+v", calls)
	}
}

func TestTheWordDiffDescribesTheChangeWithoutTheModel(t *testing.T) {
	want := summarize.FallbackChangeNote(agendadiff.Compare(changeBefore, changeAfter))
	cases := map[string]struct {
		client  llm.Client
		enabled func(context.Context, string) bool
		attempt int
	}{
		"flag off":              {llmtest.Returning("never"), changeFlag(false), 1},
		"no LLM":                {nil, changeFlag(true), 1},
		"LLM not configured":    {llmtest.Failing(llm.ErrNotConfigured), changeFlag(true), 1},
		"permanent model error": {llmtest.Failing(&llm.Error{StatusCode: 400, Err: errors.New("bad")}), changeFlag(true), 1},
		"truncated reply":       {llmtest.Failing(llm.ErrTruncated), changeFlag(true), 1},
		"retryable on the last": {llmtest.Failing(&llm.Error{Retryable: true, Err: errors.New("busy")}), changeFlag(true), checkMaxAttempts},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v1, v2 := changePair()
			st := newFakeChangeStore(v1, v2)
			w := changeWorker(st, tc.client)
			w.enabled = tc.enabled
			if err := w.Work(t.Context(), changeJob(v2.ID, tc.attempt)); err != nil {
				t.Fatal(err)
			}
			if got := st.recorded(); len(got) != 1 || got[0] != want {
				t.Errorf("recorded = %q, want %q", got, want)
			}
			if fake, ok := tc.client.(*llmtest.Fake); ok && name == "flag off" && len(fake.Calls()) != 0 {
				t.Errorf("model called with the flag off")
			}
		})
	}
}

func TestARetryableModelErrorBeforeTheLastAttemptIsRetried(t *testing.T) {
	v1, v2 := changePair()
	st := newFakeChangeStore(v1, v2)
	w := changeWorker(st, llmtest.Failing(&llm.Error{Retryable: true, Err: errors.New("busy")}))

	err := w.Work(t.Context(), changeJob(v2.ID, 1))
	if err == nil || isCancel(err) || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
	if got := st.recorded(); len(got) != 0 {
		t.Errorf("recorded = %q before the last attempt", got)
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
	if got := st.recorded(); len(got) != 1 || got[0] != summarize.RevisedNote || len(fake.Calls()) != 0 {
		t.Errorf("no differences: recorded %q, calls %d", got, len(fake.Calls()))
	}

	lone := agendaVersion(uuid.New(), 2, changeAfter)
	lone.ChangeEventPending = true
	st = newFakeChangeStore(lone)
	if err := changeWorker(st, nil).Work(t.Context(), changeJob(lone.ID, 1)); err != nil {
		t.Fatal(err)
	}
	if got := st.recorded(); len(got) != 1 || !strings.HasPrefix(got[0], "Added: ") {
		t.Errorf("without a previous version: recorded %q", got)
	}
}

func TestDescribeAgendaChangeStoreErrors(t *testing.T) {
	boom := errors.New("database down")
	cases := map[string]struct {
		configure func(*fakeChangeStore)
		id        func(v2 store.AgendaVersion) uuid.UUID
		cancel    bool
	}{
		"missing version":   {func(*fakeChangeStore) {}, func(store.AgendaVersion) uuid.UUID { return uuid.New() }, true},
		"get fails":         {func(s *fakeChangeStore) { s.getErr = boom }, nil, false},
		"previous fails":    {func(s *fakeChangeStore) { s.previousErr = boom }, nil, false},
		"record fails":      {func(s *fakeChangeStore) { s.recordErr = boom }, nil, false},
		"record finds none": {func(s *fakeChangeStore) { s.recordErr = store.ErrNotFound }, nil, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v1, v2 := changePair()
			st := newFakeChangeStore(v1, v2)
			tc.configure(st)
			id := v2.ID
			if tc.id != nil {
				id = tc.id(v2)
			}
			err := changeWorker(st, nil).Work(t.Context(), changeJob(id, 1))
			if err == nil || isCancel(err) != tc.cancel {
				t.Errorf("err = %v, want cancel %v", err, tc.cancel)
			}
		})
	}
}

type fakeChangeSweepStore struct {
	ids    []uuid.UUID
	err    error
	before time.Time
	limit  int
}

func (s *fakeChangeSweepStore) ListPendingAgendaChanges(_ context.Context, before time.Time, limit int) ([]uuid.UUID, error) {
	s.before, s.limit = before, limit
	return s.ids, s.err
}

func TestTheChangeSweepAsksForStalePendingVersions(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 30, 0, 0, time.UTC)
	st := &fakeChangeSweepStore{}
	w := &EnqueueAgendaChangesWorker{logger: discard, store: st, clock: sourcestest.NewClock(now)}
	job := &river.Job[EnqueueAgendaChangesArgs]{JobRow: &rivertype.JobRow{ID: 3}}

	if err := w.Work(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if !st.before.Equal(now.Add(-changeSweepGrace)) || st.limit != changeSweepLimit {
		t.Errorf("asked for versions before %v, limit %d", st.before, st.limit)
	}

	st.err = errors.New("database down")
	if err := w.Work(t.Context(), job); !errors.Is(err, st.err) {
		t.Errorf("err = %v, want the store error", err)
	}

	st.err, st.ids = nil, []uuid.UUID{uuid.New()}
	if err := w.Work(t.Context(), job); err == nil {
		t.Error("err = nil without a River client in the context")
	}

	args, opts := periodicChangeSweep(sourcestest.NewClock(now), "q")()
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
