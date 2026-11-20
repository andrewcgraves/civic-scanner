//go:build integration

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestCheckAgencyCommandRefusesASubmittedAgency(t *testing.T) {
	t.Setenv("DATABASE_URL", testdb.URL(t))
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("FEATURE_FLAGS", jobs.FlagCheckAgencies)
	ctx := t.Context()
	pool := testdb.Pool(t)
	queue := checkQueue(t, pool)
	s := store.New(pool)
	workosID := "user_worker_test_" + uuid.NewString()
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

	out, err := runCheck(t, queue, sub.Slug)
	if want := `agency "` + sub.Slug + `" is unknown or not live; nothing enqueued`; err == nil || !strings.HasPrefix(err.Error(), want) || out != "" {
		t.Errorf("output %q, error %v, want %q", out, err, want)
	}
	out, err = runCheck(t, queue, "--all")
	if err != nil || strings.Contains(out, sub.Slug) {
		t.Errorf("--all: output %q, error %v, want no mention of %s", out, err, sub.Slug)
	}
	if checks := queuedChecks(t, pool, queue); len(checks) == 0 {
		t.Error("--all enqueued nothing")
	} else if _, ok := checks[sub.Slug]; ok {
		t.Errorf("a check was enqueued for the submitted agency")
	}
}
