//go:build integration

package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func submitter(t *testing.T, pool *pgxpool.Pool) store.User {
	t.Helper()
	workosID := workOSUserID(t, pool)
	u, _, err := store.New(pool).EnsureUser(t.Context(), store.UserInput{WorkOSUserID: workosID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()),
			`DELETE FROM agencies WHERE id IN (SELECT agency_id FROM agency_submissions WHERE submitted_by = $1)`, u.ID)
	})
	return u
}

func databaseClock(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}

func uniqueSite() (string, string) {
	key := "civ38-" + uuid.NewString() + ".example.org"
	return "https://www." + key, key
}

func submit(t *testing.T, s *store.Store, user store.User, name string) (store.AgencySubmission, string) {
	t.Helper()
	site, key := uniqueSite()
	sub, err := s.CreateAgencySubmission(t.Context(), store.AgencySubmissionInput{UserID: user.ID, Name: name, Website: site, WebsiteKey: key})
	if err != nil {
		t.Fatalf("CreateAgencySubmission: %v", err)
	}
	return sub, key
}

func TestCreateAgencySubmissionStoresAPendingAgency(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)

	before := databaseClock(t, pool)
	sub, key := submit(t, s, user, "Big Bear Transit")
	after := databaseClock(t, pool)
	if sub.AgencyID == uuid.Nil || sub.Name != "Big Bear Transit" || sub.SubmittedBy != user.ID ||
		sub.SubmittedAt.Before(before) || sub.SubmittedAt.After(after) {
		t.Errorf("submission = %+v, want submitted between %s and %s by the database clock", sub, before, after)
	}
	if want := store.SubmittedSlug(sub.AgencyID); sub.Slug != want || !strings.HasPrefix(want, store.SubmittedSlugPrefix) || len(want) != len(store.SubmittedSlugPrefix)+32 {
		t.Errorf("slug = %q, want %q", sub.Slug, want)
	}

	var status, slug, name, website, storedKey string
	var by uuid.UUID
	var at time.Time
	if err := pool.QueryRow(ctx, `
SELECT a.status, a.slug, a.name, a.website, w.site_key, s.submitted_by, s.submitted_at
FROM agencies a
JOIN agency_websites w ON w.agency_id = a.id
JOIN agency_submissions s ON s.agency_id = a.id
WHERE a.id = $1`, sub.AgencyID).Scan(&status, &slug, &name, &website, &storedKey, &by, &at); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || slug != sub.Slug || name != sub.Name || website != sub.Website || storedKey != key || by != user.ID || !at.Equal(sub.SubmittedAt) {
		t.Errorf("row = %s %s %s %s %s %s %s", status, slug, name, website, storedKey, by, at)
	}
}

func TestPendingSubmissionIsInvisibleToEveryPublicRead(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	sub, _ := submit(t, s, user, "LA Metro")

	var meetingID uuid.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO meetings (agency_id, title, starts_at, status, source_id)
VALUES ($1, 'Board meeting', now(), 'held', 'civ38') RETURNING id`, sub.AgencyID).Scan(&meetingID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM meetings WHERE id = $1`, meetingID)
	})

	agencies, err := s.ListAgencies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agencies {
		if a.ID == sub.AgencyID {
			t.Errorf("ListAgencies includes the pending submission: %+v", a)
		}
	}
	if _, err := s.GetLiveAgency(ctx, sub.Slug); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetLiveAgency(%s) err = %v, want ErrNotFound", sub.Slug, err)
	}
	if _, err := s.GetLiveAgencyFeed(ctx, sub.Slug); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetLiveAgencyFeed err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetLiveMeetingDetail(ctx, meetingID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetLiveMeetingDetail err = %v, want ErrNotFound", err)
	}
	live, err := s.GetLiveAgency(ctx, "la-metro")
	if err != nil || live.Name != "LA Metro" || live.ID == sub.AgencyID {
		t.Errorf("the live LA Metro = %+v, %v", live, err)
	}
}

func TestSubmissionRejectsAWebsiteOnFileForEveryStatus(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	other := submitter(t, pool)

	in := store.AgencySubmissionInput{UserID: user.ID, Name: "Not LA Metro", Website: "https://metro.net", WebsiteKey: "metro.net"}
	if _, err := s.CreateAgencySubmission(ctx, in); !errors.Is(err, store.ErrWebsiteOnFile) {
		t.Errorf("live agency's site: err = %v, want ErrWebsiteOnFile", err)
	}

	pending, key := submit(t, s, user, "First")
	in = store.AgencySubmissionInput{UserID: other.ID, Name: "Second", Website: "http://" + key + "/other", WebsiteKey: key}
	if _, err := s.CreateAgencySubmission(ctx, in); !errors.Is(err, store.ErrWebsiteOnFile) {
		t.Errorf("pending agency's site: err = %v, want ErrWebsiteOnFile", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE agencies SET status = 'rejected' WHERE id = $1`, pending.AgencyID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAgencySubmission(ctx, in); !errors.Is(err, store.ErrWebsiteOnFile) {
		t.Errorf("rejected agency's site: err = %v, want ErrWebsiteOnFile", err)
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agency_submissions WHERE submitted_by = $1`, other.ID).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("rejected submissions left %d rows (%v)", rows, err)
	}
}

func TestConcurrentSubmissionsOfOneSiteCreateOneAgency(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	site, key := uniqueSite()
	const n = 12
	users := make([]store.User, n)
	for i := range users {
		users[i] = submitter(t, pool)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			_, errs[i] = s.CreateAgencySubmission(t.Context(), store.AgencySubmissionInput{UserID: users[i].ID, Name: "Racer", Website: site, WebsiteKey: key})
		})
	}
	close(start)
	wg.Wait()

	created, onFile := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, store.ErrWebsiteOnFile):
			onFile++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != 1 || onFile != n-1 {
		t.Errorf("created %d, on file %d, want 1 and %d", created, onFile, n-1)
	}
	var agencies int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agency_websites WHERE site_key = $1`, key).Scan(&agencies); err != nil || agencies != 1 {
		t.Errorf("agencies with the site = %d (%v), want 1", agencies, err)
	}
}

func TestSubmissionLimitPerUserHoldsUnderConcurrency(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	const n = 3 * store.MaxPendingSubmissionsPerUser

	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			site, key := uniqueSite()
			_, errs[i] = s.CreateAgencySubmission(ctx, store.AgencySubmissionInput{UserID: user.ID, Name: "Many", Website: site, WebsiteKey: key})
		})
	}
	close(start)
	wg.Wait()

	created, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, store.ErrSubmissionLimit):
			limited++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != store.MaxPendingSubmissionsPerUser || limited != n-created {
		t.Fatalf("created %d, limited %d, want %d and %d", created, limited, store.MaxPendingSubmissionsPerUser, n-store.MaxPendingSubmissionsPerUser)
	}

	other := submitter(t, pool)
	submit(t, s, other, "Someone else is not limited")

	if _, err := pool.Exec(ctx, `
UPDATE agencies SET status = 'rejected'
WHERE id = (SELECT agency_id FROM agency_submissions WHERE submitted_by = $1 LIMIT 1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	submit(t, s, user, "Room again after a review")
	site, key := uniqueSite()
	if _, err := s.CreateAgencySubmission(ctx, store.AgencySubmissionInput{UserID: user.ID, Name: "Over", Website: site, WebsiteKey: key}); !errors.Is(err, store.ErrSubmissionLimit) {
		t.Errorf("err = %v, want ErrSubmissionLimit", err)
	}
}

func TestCreateAgencySubmissionRefusesIncompleteInput(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	site, key := uniqueSite()
	full := store.AgencySubmissionInput{UserID: user.ID, Name: "Name", Website: site, WebsiteKey: key}
	for name, in := range map[string]store.AgencySubmissionInput{
		"no user":    {Name: full.Name, Website: full.Website, WebsiteKey: full.WebsiteKey},
		"no name":    {UserID: full.UserID, Website: full.Website, WebsiteKey: full.WebsiteKey},
		"no website": {UserID: full.UserID, Name: full.Name, WebsiteKey: full.WebsiteKey},
		"no key":     {UserID: full.UserID, Name: full.Name, Website: full.Website},
	} {
		if _, err := s.CreateAgencySubmission(t.Context(), in); !errors.Is(err, store.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}

	in := full
	in.UserID = uuid.New()
	if _, err := s.CreateAgencySubmission(t.Context(), in); err == nil || errors.Is(err, store.ErrWebsiteOnFile) {
		t.Errorf("unknown user: err = %v, want a foreign key error", err)
	}
	var left int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agency_websites WHERE site_key = $1`, key).Scan(&left); err != nil || left != 0 {
		t.Errorf("a failed submission left %d rows (%v)", left, err)
	}
}

func TestCreateAgencySubmissionFailsOnClosedPool(t *testing.T) {
	pool := testdb.Pool(t)
	user := submitter(t, pool)
	closed, err := pgxpool.New(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	site, key := uniqueSite()
	if _, err := store.New(closed).CreateAgencySubmission(t.Context(), store.AgencySubmissionInput{UserID: user.ID, Name: "x", Website: site, WebsiteKey: key}); err == nil {
		t.Error("no error on a closed pool")
	}
}

func TestEveryLiveOrPendingAgencyTheStoreKnowsHasASiteRow(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)

	created := make([]uuid.UUID, 0, 3)
	for _, name := range []string{"One", "Two", "Three"} {
		sub, _ := submit(t, s, user, name)
		created = append(created, sub.AgencyID)
	}
	if _, err := pool.Exec(ctx, `UPDATE agencies SET status = 'live' WHERE id = $1`, created[0]); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, `
SELECT a.slug
FROM agencies a
LEFT JOIN agency_websites w ON w.agency_id = a.id
WHERE a.status IN ('live', 'pending')
  AND w.agency_id IS NULL
  AND (a.id = ANY($1) OR a.slug = ANY($2))`,
		created, []string{"metrolink", "la-metro", "vctc", "rctc", "sbcta", "octa"})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			t.Fatal(err)
		}
		t.Errorf("agency %s has no agency_websites row; every path that creates an agency must insert one", slug)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
