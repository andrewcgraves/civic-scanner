//go:build integration

package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func seedMeeting(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	agencyID := seedAgency(t, pool, slug)
	m, _, err := store.New(pool).UpsertMeeting(ctx, meetingInput(agencyID, slug, time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("UpsertMeeting: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agenda_versions WHERE meeting_id = $1`, m.ID)
	})
	return m.ID
}

func agendaInput(meetingID uuid.UUID, text string) store.AgendaVersionInput {
	return store.AgendaVersionInput{
		MeetingID:   meetingID,
		SourceURL:   "https://example.com/agendas/" + meetingID.String() + ".pdf",
		Text:        text,
		ContentHash: store.HashContent(text),
	}
}

func TestAddAgendaVersionNumbersNewContentAndSkipsUnchanged(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-add")

	first, created, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda one"))
	if err != nil || !created {
		t.Fatalf("first add = (created %v, err %v), want created", created, err)
	}
	if first.ID == uuid.Nil || first.MeetingID != meetingID || first.Version != 1 ||
		first.Text != "agenda one" || first.ContentHash != store.HashContent("agenda one") ||
		first.SourceURL == "" || first.Summary != nil || first.ChangeNote != nil ||
		first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Errorf("first version = %+v", first)
	}

	again := agendaInput(meetingID, "agenda one")
	again.SourceURL = "https://example.com/moved.pdf"
	same, created, err := s.AddAgendaVersion(ctx, again)
	if err != nil || created {
		t.Fatalf("unchanged add = (created %v, err %v), want not created", created, err)
	}
	if same.ID != first.ID || same.Version != 1 || same.SourceURL != first.SourceURL {
		t.Errorf("unchanged add returned %+v, want the existing version %+v", same, first)
	}

	second, created, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda two"))
	if err != nil || !created || second.Version != 2 {
		t.Fatalf("changed add = (version %d, created %v, err %v), want version 2 created", second.Version, created, err)
	}

	reverted, created, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda one"))
	if err != nil || !created || reverted.Version != 3 {
		t.Errorf("revert to earlier content = (version %d, created %v, err %v), want version 3 created", reverted.Version, created, err)
	}
}

func TestAddAgendaVersionNumbersEachMeetingFromOne(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	a := seedMeeting(t, pool, "store-test-agenda-per-meeting-a")
	b := seedMeeting(t, pool, "store-test-agenda-per-meeting-b")

	if _, _, err := s.AddAgendaVersion(ctx, agendaInput(a, "a1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddAgendaVersion(ctx, agendaInput(a, "a2")); err != nil {
		t.Fatal(err)
	}
	got, created, err := s.AddAgendaVersion(ctx, agendaInput(b, "a2"))
	if err != nil || !created || got.Version != 1 {
		t.Errorf("other meeting = (version %d, created %v, err %v), want version 1 created", got.Version, created, err)
	}
}

func TestAddAgendaVersionForMissingMeetingIsNotFound(t *testing.T) {
	s := store.New(testdb.Pool(t))

	if _, _, err := s.AddAgendaVersion(t.Context(), agendaInput(uuid.New(), "orphan")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAddAgendaVersionRejectsEmptySourceURL(t *testing.T) {
	pool := testdb.Pool(t)
	meetingID := seedMeeting(t, pool, "store-test-agenda-no-url")

	in := agendaInput(meetingID, "agenda")
	in.SourceURL = ""
	if _, _, err := store.New(pool).AddAgendaVersion(t.Context(), in); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want a constraint error", err)
	}
}

func TestAddAgendaVersionTxCommitsAndRollsBackWithCaller(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-tx")

	begin := func() pgx.Tx {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
		return tx
	}

	rolledBack := begin()
	if _, _, err := s.AddAgendaVersionTx(ctx, rolledBack, agendaInput(meetingID, "rolled back")); err != nil {
		t.Fatalf("AddAgendaVersionTx: %v", err)
	}
	if err := rolledBack.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	committed := begin()
	v, created, err := s.AddAgendaVersionTx(ctx, committed, agendaInput(meetingID, "committed"))
	if err != nil || !created || v.Version != 1 {
		t.Fatalf("AddAgendaVersionTx = (version %d, created %v, err %v), want version 1 created", v.Version, created, err)
	}
	if _, err := s.LatestAgendaVersion(ctx, meetingID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("uncommitted version visible outside the transaction: err = %v", err)
	}
	if err := committed.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestAgendaVersion(ctx, meetingID)
	if err != nil || latest.ID != v.ID {
		t.Errorf("LatestAgendaVersion = (%+v, %v), want the committed version", latest, err)
	}
}

func TestAddAgendaVersionConcurrentAddsGetDistinctNumbers(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-concurrent")

	const writers = 12
	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		errs    = make(chan error, writers*2)
		created = make(chan int, writers*2)
	)
	for i := range writers {
		for _, useTx := range []bool{false, true} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				in := agendaInput(meetingID, fmt.Sprintf("agenda %d tx %v", i, useTx))
				<-start
				if !useTx {
					v, ok, err := s.AddAgendaVersion(ctx, in)
					if err != nil {
						errs <- err
						return
					}
					if ok {
						created <- v.Version
					}
					return
				}
				tx, err := pool.Begin(ctx)
				if err != nil {
					errs <- err
					return
				}
				v, ok, err := s.AddAgendaVersionTx(ctx, tx, in)
				if err != nil {
					_ = tx.Rollback(ctx)
					errs <- err
					return
				}
				time.Sleep(5 * time.Millisecond)
				if err := tx.Commit(ctx); err != nil {
					errs <- err
					return
				}
				if ok {
					created <- v.Version
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	close(created)

	for err := range errs {
		t.Errorf("concurrent add: %v", err)
	}
	seen := map[int]bool{}
	for v := range created {
		if seen[v] {
			t.Errorf("version %d returned twice", v)
		}
		seen[v] = true
	}
	if len(seen) != writers*2 {
		t.Errorf("created %d versions, want %d", len(seen), writers*2)
	}

	versions, err := s.ListAgendaVersions(ctx, meetingID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != writers*2 {
		t.Fatalf("stored %d versions, want %d", len(versions), writers*2)
	}
	for i, v := range versions {
		if want := writers*2 - i; v.Version != want {
			t.Errorf("versions[%d].Version = %d, want %d", i, v.Version, want)
		}
	}
}

func TestAddAgendaVersionConcurrentSameContentAddsOnce(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-concurrent-same")

	const writers = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		start   = make(chan struct{})
		created int
		ids     = map[uuid.UUID]bool{}
	)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v, ok, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "the same agenda"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("concurrent add: %v", err)
				return
			}
			if ok {
				created++
			}
			ids[v.ID] = true
		}()
	}
	close(start)
	wg.Wait()

	if created != 1 || len(ids) != 1 {
		t.Errorf("created %d versions with %d distinct ids, want 1 and 1", created, len(ids))
	}
}

func TestAddAgendaVersionFailsOnClosedPool(t *testing.T) {
	pool, err := store.Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()

	_, _, err = store.New(pool).AddAgendaVersion(t.Context(), agendaInput(uuid.New(), "agenda"))
	if err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want a connection error", err)
	}
}

func TestLatestAgendaVersionIsTheHighestNumber(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-latest")

	if _, err := s.LatestAgendaVersion(ctx, meetingID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("no versions: err = %v, want ErrNotFound", err)
	}
	for _, text := range []string{"one", "two", "three"} {
		if _, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, text)); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := s.LatestAgendaVersion(ctx, meetingID)
	if err != nil || latest.Version != 3 || latest.Text != "three" {
		t.Errorf("LatestAgendaVersion = (version %d %q, %v), want version 3 three", latest.Version, latest.Text, err)
	}
}

func TestListAgendaVersionsNewestFirst(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-list")
	other := seedMeeting(t, pool, "store-test-agenda-list-other")

	for _, text := range []string{"one", "two", "three"} {
		if _, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, text)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AddAgendaVersion(ctx, agendaInput(other, "other")); err != nil {
		t.Fatal(err)
	}

	versions, err := s.ListAgendaVersions(ctx, meetingID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range versions {
		got = append(got, fmt.Sprintf("%d:%s", v.Version, v.Text))
	}
	if want := []string{"3:three", "2:two", "1:one"}; !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}

	none, err := s.ListAgendaVersions(ctx, uuid.New())
	if err != nil || len(none) != 0 {
		t.Errorf("unknown meeting = (%v, %v), want empty", none, err)
	}
}

func TestSetAgendaSummaryAndChangeNote(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-set")
	v, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda"))
	if err != nil {
		t.Fatal(err)
	}

	summarized, err := s.SetAgendaSummary(ctx, v.ID, "The board approves the budget.")
	if err != nil {
		t.Fatalf("SetAgendaSummary: %v", err)
	}
	if summarized.Summary == nil || *summarized.Summary != "The board approves the budget." || summarized.ChangeNote != nil {
		t.Errorf("after summary: %+v", summarized)
	}
	if !summarized.UpdatedAt.After(v.UpdatedAt) || !summarized.CreatedAt.Equal(v.CreatedAt) {
		t.Errorf("timestamps after summary: created %v -> %v, updated %v -> %v", v.CreatedAt, summarized.CreatedAt, v.UpdatedAt, summarized.UpdatedAt)
	}

	noted, err := s.SetAgendaChangeNote(ctx, v.ID, "Adds item 7.")
	if err != nil {
		t.Fatalf("SetAgendaChangeNote: %v", err)
	}
	if noted.ChangeNote == nil || *noted.ChangeNote != "Adds item 7." || noted.Summary == nil || *noted.Summary != *summarized.Summary {
		t.Errorf("after change note: %+v", noted)
	}

	latest, err := s.LatestAgendaVersion(ctx, meetingID)
	if err != nil || latest.Summary == nil || latest.ChangeNote == nil || latest.Text != "agenda" || latest.Version != 1 {
		t.Errorf("LatestAgendaVersion = (%+v, %v), want both fields set on version 1", latest, err)
	}
}

func TestSetAgendaFieldsOnMissingVersionIsNotFound(t *testing.T) {
	ctx := t.Context()
	s := store.New(testdb.Pool(t))

	if _, err := s.SetAgendaSummary(ctx, uuid.New(), "s"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetAgendaSummary err = %v, want ErrNotFound", err)
	}
	if _, err := s.SetAgendaChangeNote(ctx, uuid.New(), "n"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetAgendaChangeNote err = %v, want ErrNotFound", err)
	}
}

func TestSetAgendaFieldsTxRollBackWithCaller(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	meetingID := seedMeeting(t, pool, "store-test-agenda-set-tx")
	v, _, err := s.AddAgendaVersion(ctx, agendaInput(meetingID, "agenda"))
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	if _, err := s.SetAgendaSummaryTx(ctx, tx, v.ID, "summary"); err != nil {
		t.Fatalf("SetAgendaSummaryTx: %v", err)
	}
	if _, err := s.SetAgendaChangeNoteTx(ctx, tx, v.ID, "note"); err != nil {
		t.Fatalf("SetAgendaChangeNoteTx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestAgendaVersion(ctx, meetingID)
	if err != nil || latest.Summary != nil || latest.ChangeNote != nil {
		t.Errorf("after rollback: (%+v, %v), want summary and change note unset", latest, err)
	}
}

func TestAgendaReadsAndWritesFailOnClosedPool(t *testing.T) {
	ctx := t.Context()
	pool, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	s := store.New(pool)

	failures := map[string]error{}
	_, failures["latest"] = s.LatestAgendaVersion(ctx, uuid.New())
	_, failures["list"] = s.ListAgendaVersions(ctx, uuid.New())
	_, failures["summary"] = s.SetAgendaSummary(ctx, uuid.New(), "s")
	_, failures["change note"] = s.SetAgendaChangeNote(ctx, uuid.New(), "n")
	for name, err := range failures {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want a connection error", name, err)
		}
	}
}
