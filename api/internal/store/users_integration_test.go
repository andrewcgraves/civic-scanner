//go:build integration

package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func workOSUserID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	id := "user_store_test_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE workos_user_id = $1`, id)
	})
	return id
}

func countUsers(t *testing.T, pool *pgxpool.Pool, workosID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE workos_user_id = $1`, workosID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnsureUserCreatesOnFirstSight(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	workosID := workOSUserID(t, pool)

	u, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID, Email: "ada@example.com"})
	if err != nil || !created {
		t.Fatalf("first EnsureUser = (created %v, err %v), want created", created, err)
	}
	if u.ID == uuid.Nil || u.WorkOSUserID != workosID || u.Email == nil || *u.Email != "ada@example.com" || u.CreatedAt.IsZero() || !u.UpdatedAt.Equal(u.CreatedAt) {
		t.Errorf("user = %+v", u)
	}

	again, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID, Email: "ada@example.com"})
	if err != nil || created {
		t.Fatalf("second EnsureUser = (created %v, err %v), want existing", created, err)
	}
	if again.ID != u.ID || !again.UpdatedAt.Equal(u.UpdatedAt) {
		t.Errorf("second user = %+v, want %+v", again, u)
	}
	if n := countUsers(t, pool, workosID); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestEnsureUserTracksEmailChanges(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	workosID := workOSUserID(t, pool)

	first, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID})
	if err != nil || !created || first.Email != nil {
		t.Fatalf("without email = (%+v, created %v, err %v)", first, created, err)
	}

	withEmail, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID, Email: "ada@example.com"})
	if err != nil || created || withEmail.ID != first.ID || withEmail.Email == nil || *withEmail.Email != "ada@example.com" || !withEmail.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("email added = (%+v, created %v, err %v)", withEmail, created, err)
	}

	changed, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID, Email: "ada@new.example.com"})
	if err != nil || changed.ID != first.ID || changed.Email == nil || *changed.Email != "ada@new.example.com" {
		t.Fatalf("email changed = (%+v, err %v)", changed, err)
	}

	kept, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID})
	if err != nil || created || kept.Email == nil || *kept.Email != "ada@new.example.com" || !kept.UpdatedAt.Equal(changed.UpdatedAt) {
		t.Errorf("token without email = (%+v, created %v, err %v), want the stored email kept", kept, created, err)
	}
}

func TestEnsureUserEmailIsNotAnIdentity(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	first, second := workOSUserID(t, pool), workOSUserID(t, pool)

	a, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: first, Email: "shared@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	b, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: second, Email: "shared@example.com"})
	if err != nil || !created || a.ID == b.ID {
		t.Errorf("second user with the same email = (%+v, created %v, err %v), want a new user", b, created, err)
	}
}

func TestEnsureUserConcurrentFirstSight(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	workosID := workOSUserID(t, pool)

	const workers = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ids     = map[uuid.UUID]bool{}
		creates int
	)
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start
			u, created, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID, Email: "ada@example.com"})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("EnsureUser: %v", err)
				return
			}
			ids[u.ID] = true
			if created {
				creates++
			}
		})
	}
	close(start)
	wg.Wait()

	if len(ids) != 1 || creates != 1 {
		t.Errorf("distinct ids = %d, creates = %d, want 1 and 1", len(ids), creates)
	}
	if n := countUsers(t, pool, workosID); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestEnsureUserRejectsEmptyID(t *testing.T) {
	_, _, err := store.New(testdb.Pool(t)).EnsureUser(t.Context(), store.UserInput{Email: "ada@example.com"})
	if !errors.Is(err, store.ErrInvalidUser) {
		t.Errorf("err = %v, want ErrInvalidUser", err)
	}
}

func TestEnsureUserDatabaseErrors(t *testing.T) {
	pool := testdb.Pool(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := store.New(pool).EnsureUser(ctx, store.UserInput{WorkOSUserID: "user_cancelled"}); err == nil {
		t.Error("EnsureUser with a cancelled context succeeded")
	}
	if _, _, err := store.New(pool).EnsureUser(t.Context(), store.UserInput{WorkOSUserID: workOSUserID(t, pool), Email: "nul\x00byte"}); err == nil {
		t.Error("EnsureUser with an email Postgres rejects succeeded")
	}
}
