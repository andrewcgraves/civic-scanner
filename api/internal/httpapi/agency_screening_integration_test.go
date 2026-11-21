//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestAgencySubmissionEnqueuesItsScreening(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	inserter, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	queue := "httpapi_screen_" + uuid.NewString()[:8]
	flags := func(_ context.Context, flag string) bool {
		return slices.Contains([]string{httpapi.FlagAccounts, httpapi.FlagAgencySubmissions}, flag)
	}
	screening := false
	s := newAuthServer(t, flags, httpapi.WithSubmissionFollowUp(jobs.EnqueueScreenAgency{
		Client: inserter, Queue: queue,
		Enabled: func(_ context.Context, flag string) bool { return flag == jobs.FlagScreenAgencies && screening },
	}))
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM river_job WHERE queue = $1`, queue)
		_, _ = pool.Exec(bg, `
DELETE FROM agencies WHERE id IN (
    SELECT s.agency_id FROM agency_submissions s JOIN users u ON u.id = s.submitted_by WHERE u.workos_user_id = $1
)`, s.users)
	})
	token := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))

	submit := func() uuid.UUID {
		t.Helper()
		host := "civ47-" + uuid.NewString() + ".example.org"
		status, _, raw := postJSONWithToken(t, s.url+"/v1/agency-submissions", token, `{"name":"Screened Transit","website":"`+host+`"}`)
		if status != http.StatusCreated {
			t.Fatalf("status = %d, body %s", status, raw)
		}
		var created v1.AgencySubmission
		if err := json.Unmarshal(raw, &created); err != nil {
			t.Fatal(err)
		}
		return created.Id
	}
	jobsFor := func(id uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE queue = $1 AND kind = 'screen_agency' AND args->>'agency_id' = $2`, queue, id.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if id := submit(); jobsFor(id) != 0 {
		t.Error("a screening was enqueued with agency-screening off")
	}
	screening = true
	if id := submit(); jobsFor(id) != 1 {
		t.Error("no screening enqueued with agency-screening on")
	}
}
