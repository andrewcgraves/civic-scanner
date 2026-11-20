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

func postWithToken(t *testing.T, url, token, body string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, resp.Header, raw
}

func TestAgencySubmissionAgainstPostgres(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	flags := func(_ context.Context, flag string) bool {
		return slices.Contains([]string{httpapi.FlagAccounts, httpapi.FlagAgencySubmissions, httpapi.FlagAgencyFeeds}, flag)
	}
	s := newAuthServer(t, flags, httpapi.WithPublicWebURL("https://civicscanner.example.org"))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `
DELETE FROM agencies WHERE id IN (
    SELECT s.agency_id FROM agency_submissions s JOIN users u ON u.id = s.submitted_by WHERE u.workos_user_id = $1
)`, s.users)
	})
	token := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))
	host := "civ38-" + uuid.NewString() + ".example.org"

	status, _, _ := postWithToken(t, s.url+"/v1/agency-submissions", "", `{"name":"Big Bear Transit","website":"`+host+`"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("without a token: status %d, want 401", status)
	}

	status, header, raw := postWithToken(t, s.url+"/v1/agency-submissions", token, `{"name":" Big Bear  Transit ","website":"HTTPS://WWW.`+strings.ToUpper(host)+`/board/?ref=1"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, body %s", status, raw)
	}
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", header.Get("Cache-Control"))
	}
	var created v1.AgencySubmission
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "Big Bear Transit" || created.Website != "https://www."+host+"/board" || created.Status != v1.AgencySubmissionStatusPending {
		t.Errorf("created = %+v", created)
	}

	var by, status2 string
	if err := pool.QueryRow(ctx, `
SELECT u.workos_user_id, a.status FROM agency_submissions s
JOIN users u ON u.id = s.submitted_by JOIN agencies a ON a.id = s.agency_id
WHERE s.agency_id = $1`, created.Id).Scan(&by, &status2); err != nil {
		t.Fatal(err)
	}
	if by != s.users || status2 != "pending" {
		t.Errorf("stored submitter %q, status %q", by, status2)
	}

	for _, website := range []string{"http://" + host, host + ":443/", "https://www." + host + "/other/page"} {
		status, _, raw := postWithToken(t, s.url+"/v1/agency-submissions", token, `{"name":"Again","website":"`+website+`"}`)
		if status != http.StatusConflict || !strings.Contains(string(raw), `"already_on_file"`) || strings.Contains(string(raw), "Big Bear") {
			t.Errorf("%s: status %d, body %s, want 409 already_on_file", website, status, raw)
		}
	}
	status, _, raw = postWithToken(t, s.url+"/v1/agency-submissions", token, `{"name":"Metro copy","website":"https://www.metro.net/about"}`)
	if status != http.StatusConflict {
		t.Errorf("a live agency's site: status %d, body %s", status, raw)
	}
	status, _, raw = postWithToken(t, s.url+"/v1/agency-submissions", token, `{"name":"x","website":"http://192.168.1.1"}`)
	if status != http.StatusBadRequest || !strings.Contains(string(raw), `"too_short"`) || !strings.Contains(string(raw), `"ip_address"`) {
		t.Errorf("invalid: status %d, body %s", status, raw)
	}

	var meetingID uuid.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO meetings (agency_id, title, starts_at, status, source_id)
VALUES ($1, 'Board meeting', now(), 'held', 'civ38-api') RETURNING id`, created.Id).Scan(&meetingID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM meetings WHERE id = $1`, meetingID)
	})
	slug := store.SubmittedSlug(created.Id)

	var list v1.AgencyList
	getJSON(t, s.url+"/v1/agencies", http.StatusOK, &list)
	for _, a := range list.Agencies {
		if a.Id == created.Id || strings.Contains(a.Website, host) {
			t.Errorf("GET /v1/agencies lists the pending agency: %+v", a)
		}
	}
	for _, path := range []string{
		"/v1/agencies/" + slug,
		"/v1/agencies/" + slug + "/meetings",
		"/v1/agencies/" + slug + "/feed.xml",
		"/v1/meetings/" + meetingID.String(),
	} {
		for _, bearer := range []string{"", token} {
			status, _, raw := getWithToken(t, s.url+path, bearer)
			if status != http.StatusNotFound {
				t.Errorf("GET %s (token %v): status %d, body %s, want 404", path, bearer != "", status, raw)
			}
		}
	}
}
