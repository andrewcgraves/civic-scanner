//go:build integration

package store_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func liveSlug() string {
	return "civ48-" + uuid.NewString()[:8]
}

func pendingSubmissionIDs(t *testing.T, s *store.Store, among []uuid.UUID) []uuid.UUID {
	t.Helper()
	pending, err := s.ListPendingAgencySubmissions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for _, p := range pending {
		if slices.Contains(among, p.AgencyID) {
			ids = append(ids, p.AgencyID)
		}
	}
	return ids
}

func TestListPendingAgencySubmissionsOldestFirstWithSubmitter(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	email := "civ48-" + uuid.NewString() + "@example.com"
	if _, err := pool.Exec(ctx, `UPDATE users SET email = $2 WHERE id = $1`, user.ID, email); err != nil {
		t.Fatal(err)
	}

	first, _ := submit(t, s, user, "First")
	second, _ := submit(t, s, user, "Second")
	third, _ := submit(t, s, user, "Third")
	ids := []uuid.UUID{first.AgencyID, second.AgencyID, third.AgencyID}
	for i, at := range []string{"2001-01-01T00:00:02Z", "2001-01-01T00:00:03Z", "2001-01-01T00:00:01Z"} {
		if _, err := pool.Exec(ctx, `UPDATE agency_submissions SET submitted_at = $2 WHERE agency_id = $1`, ids[i], at); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := pendingSubmissionIDs(t, s, ids), []uuid.UUID{third.AgencyID, first.AgencyID, second.AgencyID}; !slices.Equal(got, want) {
		t.Fatalf("pending order = %v, want %v", got, want)
	}

	pending, err := s.ListPendingAgencySubmissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.AgencyID != first.AgencyID {
			continue
		}
		if p.Name != "First" || p.Website != first.Website || p.SubmittedBy != user.ID || p.SubmitterEmail == nil || *p.SubmitterEmail != email ||
			!p.SubmittedAt.Equal(time.Date(2001, 1, 1, 0, 0, 2, 0, time.UTC)) {
			t.Errorf("pending = %+v", p)
		}
	}

	if _, err := s.RejectAgencySubmission(ctx, second.AgencyID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveAgencySubmission(ctx, third.AgencyID, liveSlug()); err != nil {
		t.Fatal(err)
	}
	if got := pendingSubmissionIDs(t, s, ids); !slices.Equal(got, []uuid.UUID{first.AgencyID}) {
		t.Errorf("after review pending = %v, want only the first", got)
	}
}

func TestListPendingAgencySubmissionsIsCapped(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	for range store.MaxPendingSubmissionsListed/store.MaxPendingSubmissionsPerUser + 1 {
		user := submitter(t, pool)
		for range store.MaxPendingSubmissionsPerUser {
			submit(t, s, user, "Capped")
		}
	}
	pending, err := s.ListPendingAgencySubmissions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != store.MaxPendingSubmissionsListed {
		t.Errorf("listed %d, want %d", len(pending), store.MaxPendingSubmissionsListed)
	}
}

func TestApproveAgencySubmissionMakesItLive(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	sub, key := submit(t, s, user, "Big Bear Transit")
	slug := liveSlug()

	reviewed, err := s.ApproveAgencySubmission(ctx, sub.AgencyID, slug)
	if err != nil {
		t.Fatal(err)
	}
	want := store.ReviewedAgency{ID: sub.AgencyID, Slug: slug, Name: "Big Bear Transit", Website: sub.Website, Status: store.AgencyStatusLive}
	if reviewed != want {
		t.Errorf("reviewed = %+v, want %+v", reviewed, want)
	}

	live, err := s.GetLiveAgency(ctx, slug)
	if err != nil || live.ID != sub.AgencyID {
		t.Fatalf("GetLiveAgency(%s) = %+v, %v", slug, live, err)
	}
	if _, err := s.GetLiveAgency(ctx, sub.Slug); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the submitted slug still resolves: %v", err)
	}
	agencies, err := s.ListAgencies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(agencies, func(a store.Agency) bool { return a.ID == sub.AgencyID }) {
		t.Error("approved agency is not in ListAgencies")
	}

	var siteKey string
	var submittedBy uuid.UUID
	if err := pool.QueryRow(ctx, `
SELECT w.site_key, s.submitted_by FROM agency_websites w JOIN agency_submissions s ON s.agency_id = w.agency_id
WHERE w.agency_id = $1`, sub.AgencyID).Scan(&siteKey, &submittedBy); err != nil {
		t.Fatal(err)
	}
	if siteKey != key || submittedBy != user.ID {
		t.Errorf("site key %q, submitted by %s; want %q and %s", siteKey, submittedBy, key, user.ID)
	}

	if _, err := s.ApproveAgencySubmission(ctx, sub.AgencyID, liveSlug()); !errors.Is(err, store.ErrNotPending) {
		t.Errorf("approving again: %v, want ErrNotPending", err)
	}
	if _, err := s.RejectAgencySubmission(ctx, sub.AgencyID); !errors.Is(err, store.ErrNotPending) {
		t.Errorf("rejecting a live agency: %v, want ErrNotPending", err)
	}
}

func TestApproveFreesASubmissionSlot(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	var last store.AgencySubmission
	for range store.MaxPendingSubmissionsPerUser {
		last, _ = submit(t, s, user, "Slot")
	}
	site, key := uniqueSite()
	in := store.AgencySubmissionInput{UserID: user.ID, Name: "One more", Website: site, WebsiteKey: key}
	if _, err := s.CreateAgencySubmission(ctx, in); !errors.Is(err, store.ErrSubmissionLimit) {
		t.Fatalf("at the limit: %v", err)
	}
	if _, err := s.ApproveAgencySubmission(ctx, last.AgencyID, liveSlug()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAgencySubmission(ctx, in); err != nil {
		t.Errorf("after an approval: %v", err)
	}
}

func TestRejectAgencySubmissionHidesItAndKeepsTheSite(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	sub, key := submit(t, s, user, "Rejected Transit")

	reviewed, err := s.RejectAgencySubmission(ctx, sub.AgencyID)
	if err != nil {
		t.Fatal(err)
	}
	want := store.ReviewedAgency{ID: sub.AgencyID, Slug: sub.Slug, Name: "Rejected Transit", Website: sub.Website, Status: store.AgencyStatusRejected}
	if reviewed != want {
		t.Errorf("reviewed = %+v, want %+v", reviewed, want)
	}
	if _, err := s.GetLiveAgency(ctx, sub.Slug); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rejected agency resolves: %v", err)
	}
	_, err = s.CreateAgencySubmission(ctx, store.AgencySubmissionInput{UserID: user.ID, Name: "Again", Website: "https://" + key, WebsiteKey: key})
	if !errors.Is(err, store.ErrWebsiteOnFile) {
		t.Errorf("resubmitting a rejected site: %v, want ErrWebsiteOnFile", err)
	}
	if _, err := s.ApproveAgencySubmission(ctx, sub.AgencyID, liveSlug()); !errors.Is(err, store.ErrNotPending) {
		t.Errorf("approving a rejected agency: %v, want ErrNotPending", err)
	}
	if _, err := s.RejectAgencySubmission(ctx, sub.AgencyID); !errors.Is(err, store.ErrNotPending) {
		t.Errorf("rejecting twice: %v, want ErrNotPending", err)
	}
}

func TestApproveAgencySubmissionRefusesATakenSlug(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	first, _ := submit(t, s, user, "First")
	second, _ := submit(t, s, user, "Second")
	slug := liveSlug()
	if _, err := s.ApproveAgencySubmission(ctx, first.AgencyID, slug); err != nil {
		t.Fatal(err)
	}

	for _, taken := range []string{slug, "la-metro"} {
		if _, err := s.ApproveAgencySubmission(ctx, second.AgencyID, taken); !errors.Is(err, store.ErrSlugTaken) {
			t.Errorf("slug %s: %v, want ErrSlugTaken", taken, err)
		}
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM agencies WHERE id = $1`, second.AgencyID).Scan(&status); err != nil || status != "pending" {
		t.Errorf("after a taken slug: status %q, %v; want pending", status, err)
	}
}

func TestReviewAgencySubmissionRefusesWhatIsNotASubmission(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	var metro uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM agencies WHERE slug = 'la-metro'`).Scan(&metro); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{uuid.New(), metro} {
		if _, err := s.ApproveAgencySubmission(ctx, id, liveSlug()); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("approve %s: %v, want ErrNotFound", id, err)
		}
		if _, err := s.RejectAgencySubmission(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("reject %s: %v, want ErrNotFound", id, err)
		}
	}
}

func TestApproveAgencySubmissionRefusesAnInvalidSlug(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	sub, _ := submit(t, s, user, "Invalid")
	for _, slug := range []string{"", "Big-Bear", "submitted-abc", "a"} {
		if _, err := s.ApproveAgencySubmission(t.Context(), sub.AgencyID, slug); !errors.Is(err, store.ErrInvalidSlug) {
			t.Errorf("slug %q: %v, want ErrInvalidSlug", slug, err)
		}
	}
}

func TestConcurrentReviewsDecideOnce(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	sub, _ := submit(t, s, user, "Contested")

	const reviewers = 10
	errs := make([]error, reviewers)
	var wg sync.WaitGroup
	for i := range reviewers {
		wg.Go(func() {
			if i%2 == 0 {
				_, errs[i] = s.ApproveAgencySubmission(ctx, sub.AgencyID, liveSlug())
				return
			}
			_, errs[i] = s.RejectAgencySubmission(ctx, sub.AgencyID)
		})
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case !errors.Is(err, store.ErrNotPending):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d reviews succeeded, want 1", succeeded)
	}
}

func TestConcurrentApprovalsShareOneSlug(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)
	slug := liveSlug()
	ids := make([]uuid.UUID, store.MaxPendingSubmissionsPerUser)
	for i := range ids {
		sub, _ := submit(t, s, user, "Same slug")
		ids[i] = sub.AgencyID
	}

	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() { _, errs[i] = s.ApproveAgencySubmission(ctx, id, slug) })
	}
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case !errors.Is(err, store.ErrSlugTaken):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d approvals succeeded, want 1", succeeded)
	}
}
