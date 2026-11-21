//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestAgencyReviewAgainstPostgres(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	flags := func(_ context.Context, flag string) bool {
		return slices.Contains([]string{httpapi.FlagAccounts, httpapi.FlagAgencySubmissions}, flag)
	}
	s := newAuthServer(t, flags)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `
DELETE FROM agencies WHERE id IN (
    SELECT s.agency_id FROM agency_submissions s JOIN users u ON u.id = s.submitted_by WHERE u.workos_user_id = $1
)`, s.users)
	})
	submitterToken := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))
	adminWorkOSID := "user_civ48_admin_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id = $1`, adminWorkOSID)
	})
	adminToken := signedToken(t, s.key, "k1", s.claims(adminWorkOSID, 5*time.Minute))

	var submitted []v1.AgencySubmission
	for _, name := range []string{"Approve Me", "Reject Me"} {
		host := "civ48-" + uuid.NewString() + ".example.org"
		status, _, raw := postJSONWithToken(t, s.url+"/v1/agency-submissions", submitterToken, `{"name":"`+name+`","website":"`+host+`"}`)
		if status != http.StatusCreated {
			t.Fatalf("submit %s: status %d, body %s", name, status, raw)
		}
		var created v1.AgencySubmission
		if err := json.Unmarshal(raw, &created); err != nil {
			t.Fatal(err)
		}
		submitted = append(submitted, created)
		if _, err := pool.Exec(ctx, `UPDATE agency_submissions SET submitted_at = '2000-01-01T00:00:00Z'::timestamptz + $2 * interval '1 second' WHERE agency_id = $1`,
			created.Id, len(submitted)); err != nil {
			t.Fatal(err)
		}
	}
	approve, reject := submitted[0], submitted[1]

	status, header, raw := getWithToken(t, s.url+"/v1/admin/agency-submissions", submitterToken)
	if status != http.StatusForbidden || !strings.Contains(string(raw), `"forbidden"`) || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("a user lists: status %d, body %s", status, raw)
	}
	status, _, raw = postJSONWithToken(t, s.url+"/v1/admin/agency-submissions/"+approve.Id.String()+"/approve", submitterToken, `{"slug":"civ48-sneaky"}`)
	if status != http.StatusForbidden {
		t.Fatalf("a user approves: status %d, body %s", status, raw)
	}

	status, _, raw = getWithToken(t, s.url+"/v1/me", adminToken)
	var me v1.User
	if err := json.Unmarshal(raw, &me); err != nil || status != http.StatusOK {
		t.Fatalf("GET /v1/me as the admin: %d %s", status, raw)
	}
	if _, err := store.New(pool).SetUserRole(ctx, store.UserRef{ID: me.Id}, store.RoleAdmin, time.Second); err != nil {
		t.Fatal(err)
	}

	status, header, raw = getWithToken(t, s.url+"/v1/admin/agency-submissions", adminToken)
	if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("admin lists: status %d, body %s", status, raw)
	}
	var list v1.PendingAgencySubmissionList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	var ours []v1.PendingAgencySubmission
	for _, p := range list.Submissions {
		if p.Id == approve.Id || p.Id == reject.Id {
			ours = append(ours, p)
		}
	}
	if len(ours) != 2 || ours[0].Id != approve.Id || ours[1].Id != reject.Id {
		t.Fatalf("listed %+v, want both submissions oldest first", ours)
	}
	if ours[0].Name != "Approve Me" || ours[0].Website != approve.Website || ours[0].SubmittedBy.Email == nil || *ours[0].SubmittedBy.Email != "ada@example.com" ||
		!ours[0].SubmittedAt.Equal(time.Date(2000, 1, 1, 0, 0, 1, 0, time.UTC)) {
		t.Errorf("listed %+v", ours[0])
	}
	if strings.Contains(string(raw), store.SubmittedSlug(approve.Id)) || strings.Contains(string(raw), s.users) {
		t.Errorf("the list carries a submitted slug or a WorkOS id: %s", raw)
	}

	slug := "civ48-" + uuid.NewString()[:8]
	status, header, raw = postJSONWithToken(t, s.url+"/v1/admin/agency-submissions/"+approve.Id.String()+"/approve", adminToken, `{"slug":"`+slug+`"}`)
	if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("approve: status %d, body %s", status, raw)
	}
	var approved v1.ReviewedAgencySubmission
	if err := json.Unmarshal(raw, &approved); err != nil {
		t.Fatal(err)
	}
	if approved != (v1.ReviewedAgencySubmission{Id: approve.Id, Slug: slug, Name: "Approve Me", Website: approve.Website, Status: v1.ReviewedAgencySubmissionStatusLive}) {
		t.Errorf("approved = %+v", approved)
	}

	var agency v1.Agency
	getJSON(t, s.url+"/v1/agencies/"+slug, http.StatusOK, &agency)
	if agency.Id != approve.Id || agency.Name != "Approve Me" {
		t.Errorf("GET /v1/agencies/%s = %+v", slug, agency)
	}
	if status, _, _ := getWithToken(t, s.url+"/v1/agencies/"+store.SubmittedSlug(approve.Id), ""); status != http.StatusNotFound {
		t.Errorf("the submitted slug still resolves: %d", status)
	}

	for _, tt := range []struct {
		id         uuid.UUID
		slug, want string
		status     int
	}{
		{approve.Id, "civ48-" + uuid.NewString()[:8], "not_pending", http.StatusConflict},
		{reject.Id, slug, "slug_taken", http.StatusConflict},
		{reject.Id, "la-metro", "slug_taken", http.StatusConflict},
		{reject.Id, "Not A Slug", "invalid_slug", http.StatusBadRequest},
		{uuid.New(), "civ48-" + uuid.NewString()[:8], "not_found", http.StatusNotFound},
	} {
		status, _, raw := postJSONWithToken(t, s.url+"/v1/admin/agency-submissions/"+tt.id.String()+"/approve", adminToken, `{"slug":"`+tt.slug+`"}`)
		if status != tt.status || !strings.Contains(string(raw), `"`+tt.want+`"`) {
			t.Errorf("approve %s as %s: status %d, body %s, want %d %s", tt.id, tt.slug, status, raw, tt.status, tt.want)
		}
	}

	status, _, raw = postJSONWithToken(t, s.url+"/v1/admin/agency-submissions/"+reject.Id.String()+"/reject", adminToken, "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"rejected"`) {
		t.Fatalf("reject: status %d, body %s", status, raw)
	}
	status, _, raw = postJSONWithToken(t, s.url+"/v1/admin/agency-submissions/"+reject.Id.String()+"/reject", adminToken, "")
	if status != http.StatusConflict || !strings.Contains(string(raw), `"not_pending"`) {
		t.Errorf("reject again: status %d, body %s", status, raw)
	}

	status, _, raw = getWithToken(t, s.url+"/v1/admin/agency-submissions", adminToken)
	list = v1.PendingAgencySubmissionList{}
	if err := json.Unmarshal(raw, &list); err != nil || status != http.StatusOK {
		t.Fatalf("list after review: %d %s", status, raw)
	}
	for _, p := range list.Submissions {
		if p.Id == approve.Id || p.Id == reject.Id {
			t.Errorf("a reviewed agency is still listed: %+v", p)
		}
	}
	var agencies v1.AgencyList
	getJSON(t, s.url+"/v1/agencies", http.StatusOK, &agencies)
	listed := map[uuid.UUID]bool{}
	for _, a := range agencies.Agencies {
		listed[a.Id] = true
	}
	if !listed[approve.Id] || listed[reject.Id] {
		t.Errorf("public list has approved %v, rejected %v; want only the approved one", listed[approve.Id], listed[reject.Id])
	}

	status, _, raw = postJSONWithToken(t, s.url+"/v1/agency-submissions", submitterToken, `{"name":"Again","website":"`+reject.Website+`"}`)
	if status != http.StatusConflict {
		t.Errorf("resubmitting a rejected site: status %d, body %s", status, raw)
	}
}
