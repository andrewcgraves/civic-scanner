//go:build integration

package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestSubmittedAgencyIsNeverChecked(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	workosID := "user_jobs_test_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id = $1`, workosID)
	})
	user, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workosID})
	if err != nil {
		t.Fatal(err)
	}
	key := "civ38-" + uuid.NewString() + ".example.org"
	sub, err := s.CreateAgencySubmission(ctx, store.AgencySubmissionInput{UserID: user.ID, Name: "Submitted", Website: "https://" + key, WebsiteKey: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE id = $1`, sub.AgencyID)
	})

	fake := sourcestest.Returning(boardMeeting("m-1", fixedNow, sources.StatusScheduled))
	h := start(t, pool, jobs.Config{Registry: registry(map[string]sources.Adapter{sub.Slug: fake})})

	if !h.enqueueChecks(time.Now().UTC().Truncate(24 * time.Hour).Add(2 * time.Hour)) {
		t.Fatal("enqueue skipped as duplicate")
	}
	if n := h.checkJobsBySlug()[sub.Slug]; n != 0 {
		t.Errorf("check jobs for the submitted agency = %d, want 0", n)
	}

	h.checkAndWait(sub.Slug, day(0), "completed")
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("adapter of the submitted agency called %d times", len(calls))
	}
	if n := len(meetingsOf(t, pool, sub.AgencyID)); n != 0 {
		t.Errorf("meetings written for the submitted agency: %d", n)
	}
}
