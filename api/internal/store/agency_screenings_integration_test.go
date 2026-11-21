//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type followUpFunc func(ctx context.Context, tx pgx.Tx, sub store.AgencySubmission) error

func (f followUpFunc) AgencySubmitted(ctx context.Context, tx pgx.Tx, sub store.AgencySubmission) error {
	return f(ctx, tx, sub)
}

func TestAgencyScreeningRoundTrip(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	sub, _ := submit(t, s, submitter(t, pool), "Screened")

	if _, err := s.GetAgencyScreening(ctx, sub.AgencyID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("before screening: err = %v, want ErrNotFound", err)
	}
	target, err := s.GetScreeningTarget(ctx, sub.AgencyID)
	if err != nil || target.Status != "pending" || target.Website != sub.Website {
		t.Fatalf("target = %+v, err %v", target, err)
	}

	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	complete := store.AgencyScreening{
		AgencyID:   sub.AgencyID,
		Status:     store.ScreeningComplete,
		Verdict:    "agendas_only",
		Evidence:   []store.ScreeningEvidence{{Supports: "agendas", Signal: "legistar", URL: "https://metro.legistar.com/", Page: sub.Website}},
		Pages:      []store.ScreeningPage{{URL: sub.Website}, {URL: sub.Website + "/gone", Error: "404"}},
		ScreenedAt: at,
	}
	if err := s.SaveAgencyScreening(ctx, complete); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgencyScreening(ctx, sub.AgencyID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "complete" || got.Verdict != "agendas_only" || got.Failure != "" || !got.ScreenedAt.Equal(at) ||
		len(got.Evidence) != 1 || got.Evidence[0] != complete.Evidence[0] || len(got.Pages) != 2 || got.Pages[1] != complete.Pages[1] {
		t.Errorf("got %+v", got)
	}

	failed := store.AgencyScreening{AgencyID: sub.AgencyID, Status: store.ScreeningFailed, Failure: "home page: 404", ScreenedAt: at.Add(time.Hour)}
	if err := s.SaveAgencyScreening(ctx, failed); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAgencyScreening(ctx, sub.AgencyID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Verdict != "" || got.Failure != "home page: 404" || len(got.Evidence) != 0 || len(got.Pages) != 0 {
		t.Errorf("after a failed screening: %+v", got)
	}
}

func TestSaveAgencyScreeningRefusesInconsistentRows(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	sub, _ := submit(t, s, submitter(t, pool), "Inconsistent")
	now := time.Now()

	for name, in := range map[string]store.AgencyScreening{
		"complete without a verdict": {AgencyID: sub.AgencyID, Status: store.ScreeningComplete, ScreenedAt: now},
		"failed with a verdict":      {AgencyID: sub.AgencyID, Status: store.ScreeningFailed, Verdict: "both", Failure: "x", ScreenedAt: now},
		"failed without a reason":    {AgencyID: sub.AgencyID, Status: store.ScreeningFailed, ScreenedAt: now},
		"failed with evidence": {
			AgencyID: sub.AgencyID, Status: store.ScreeningFailed, Failure: "x", ScreenedAt: now,
			Evidence: []store.ScreeningEvidence{{Supports: "agendas", URL: "https://a.example.org/a.pdf"}},
		},
		"unknown verdict": {AgencyID: sub.AgencyID, Status: store.ScreeningComplete, Verdict: "maybe", ScreenedAt: now},
		"unknown status":  {AgencyID: sub.AgencyID, Status: "running", ScreenedAt: now},
	} {
		if err := s.SaveAgencyScreening(ctx, in); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	if err := s.SaveAgencyScreening(ctx, store.AgencyScreening{AgencyID: uuid.New(), Status: store.ScreeningFailed, Failure: "x", ScreenedAt: now}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown agency: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetScreeningTarget(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown target: err = %v, want ErrNotFound", err)
	}
}

func TestAgencyScreeningIsDeletedWithTheAgency(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	sub, _ := submit(t, s, submitter(t, pool), "Deleted")
	if err := s.SaveAgencyScreening(ctx, store.AgencyScreening{AgencyID: sub.AgencyID, Status: store.ScreeningComplete, Verdict: "neither", ScreenedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM agencies WHERE id = $1`, sub.AgencyID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgencyScreening(ctx, sub.AgencyID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateAgencySubmissionRunsTheFollowUpInItsTransaction(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := submitter(t, pool)

	var seen store.AgencySubmission
	site, key := uniqueSite()
	sub, err := s.CreateAgencySubmission(ctx, store.AgencySubmissionInput{
		UserID: user.ID, Name: "Followed", Website: site, WebsiteKey: key,
		FollowUp: followUpFunc(func(ctx context.Context, tx pgx.Tx, sub store.AgencySubmission) error {
			seen = sub
			var status string
			return tx.QueryRow(ctx, `SELECT status FROM agencies WHERE id = $1`, sub.AgencyID).Scan(&status)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen.AgencyID != sub.AgencyID || seen.SubmittedAt.IsZero() {
		t.Errorf("follow-up saw %+v, want %+v", seen, sub)
	}

	boom := errors.New("enqueue failed")
	site, key = uniqueSite()
	_, err = s.CreateAgencySubmission(ctx, store.AgencySubmissionInput{
		UserID: user.ID, Name: "Rolled back", Website: site, WebsiteKey: key,
		FollowUp: followUpFunc(func(context.Context, pgx.Tx, store.AgencySubmission) error { return boom }),
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the follow-up's error", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agency_websites WHERE site_key = $1`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("site rows after a failed follow-up: %d, want 0", n)
	}
}
