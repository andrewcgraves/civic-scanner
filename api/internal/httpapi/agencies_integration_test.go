//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func seedAgency(t *testing.T, pool *pgxpool.Pool, slug, status string) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, $2, 'https://example.com') RETURNING id`,
		slug, status).Scan(&id); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM meetings WHERE agency_id = $1`, id)
		_, _ = pool.Exec(bg, `DELETE FROM agencies WHERE id = $1`, id)
	})
	return id
}

func seedMeeting(t *testing.T, pool *pgxpool.Pool, agencyID uuid.UUID, sourceID string, startsAt time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO meetings (agency_id, title, starts_at, status, source_url, source_id) VALUES ($1, $2, $3, 'scheduled', $4, $2) RETURNING id`,
		agencyID, sourceID, startsAt, "https://example.com/"+sourceID).Scan(&id); err != nil {
		t.Fatalf("insert meeting: %v", err)
	}
	return id
}

func newServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.NewHandler(store.New(pool), logger, "test"), logger))
	t.Cleanup(srv.Close)
	return srv
}

func getBody(t *testing.T, rawURL string) (int, string) {
	t.Helper()
	resp, err := http.Get(rawURL) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func meetingsURL(srv *httptest.Server, slug, cursor string, limit int) string {
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return srv.URL + "/v1/agencies/" + slug + "/meetings?" + q.Encode()
}

func TestGetAgencyAgainstPostgres(t *testing.T) {
	pool := testdb.Pool(t)
	id := seedAgency(t, pool, "it-get-live", "live")
	seedAgency(t, pool, "it-get-pending", "pending")
	seedAgency(t, pool, "it-get-rejected", "rejected")
	srv := newServer(t, pool)

	var agency v1.Agency
	getJSON(t, srv.URL+"/v1/agencies/it-get-live", http.StatusOK, &agency)
	if want := (v1.Agency{Id: id, Slug: "it-get-live", Name: "it-get-live", Website: "https://example.com"}); agency != want {
		t.Errorf("agency = %+v, want %+v", agency, want)
	}

	_, unknown := getBody(t, srv.URL+"/v1/agencies/it-get-unknown")
	for _, path := range []string{"/v1/agencies/it-get-unknown", "/v1/agencies/it-get-pending", "/v1/agencies/it-get-rejected", "/v1/agencies/it-get-pending/meetings", "/v1/agencies/it-get-unknown/meetings"} {
		code, body := getBody(t, srv.URL+path)
		if code != http.StatusNotFound || body != unknown {
			t.Errorf("GET %s = %d %q, want 404 %q", path, code, body, unknown)
		}
	}
}

func TestAgencyWithNoMeetingsListsEmpty(t *testing.T) {
	pool := testdb.Pool(t)
	seedAgency(t, pool, "it-meetings-none", "live")
	srv := newServer(t, pool)

	code, body := getBody(t, srv.URL+"/v1/agencies/it-meetings-none/meetings")
	if want := `{"meetings":[],"nextCursor":null}` + "\n"; code != http.StatusOK || body != want {
		t.Errorf("got %d %q, want 200 %q", code, body, want)
	}
}

func TestListAgencyMeetingsWalksEveryPageAcrossTiesAndInserts(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "it-meetings-walk", "live")
	other := seedAgency(t, pool, "it-meetings-walk-other", "live")
	srv := newServer(t, pool)

	base := time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)
	seeded := map[uuid.UUID]bool{}
	for i := range 9 {
		seeded[seedMeeting(t, pool, agencyID, fmt.Sprintf("walk-%d", i), base.Add(time.Duration(i/3)*24*time.Hour))] = true
	}
	seedMeeting(t, pool, other, "walk-other", base)

	const limit = 2
	var walked []v1.MeetingSummary
	var olderInsert, newerInsert uuid.UUID
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("pagination did not terminate")
		}
		var page v1.MeetingList
		getJSON(t, meetingsURL(srv, "it-meetings-walk", cursor, limit), http.StatusOK, &page)
		if len(page.Meetings) > limit {
			t.Fatalf("page %d has %d meetings, limit %d", pages, len(page.Meetings), limit)
		}
		walked = append(walked, page.Meetings...)

		if pages == 0 {
			newerInsert = seedMeeting(t, pool, agencyID, "walk-inserted-newer", base.Add(30*24*time.Hour))
			seedMeeting(t, pool, agencyID, "walk-inserted-tie", base.Add(24*time.Hour))
			olderInsert = seedMeeting(t, pool, agencyID, "walk-inserted-older", base.Add(-24*time.Hour))
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}

	seen := map[uuid.UUID]bool{}
	for i, m := range walked {
		if seen[m.Id] {
			t.Errorf("meeting %s repeated at %d", m.Id, i)
		}
		seen[m.Id] = true
		if i > 0 {
			prev := walked[i-1]
			if m.StartsAt.After(prev.StartsAt) || (m.StartsAt.Equal(prev.StartsAt) && bytes.Compare(m.Id[:], prev.Id[:]) >= 0) {
				t.Errorf("meeting %d (%s %s) is not after meeting %d (%s %s) in newest-first order", i, m.StartsAt, m.Id, i-1, prev.StartsAt, prev.Id)
			}
		}
	}
	for id := range seeded {
		if !seen[id] {
			t.Errorf("seeded meeting %s was skipped", id)
		}
	}
	if !seen[olderInsert] {
		t.Error("meeting inserted after the cursor position was skipped")
	}
	if seen[newerInsert] {
		t.Error("meeting inserted before the cursor position appeared on a later page")
	}
}

func TestListAgencyMeetingsRejectsBadCursorsAndLimits(t *testing.T) {
	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "it-meetings-bad", "live")
	otherID := seedAgency(t, pool, "it-meetings-bad-other", "live")
	base := time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)
	for i := range 3 {
		seedMeeting(t, pool, agencyID, fmt.Sprintf("bad-%d", i), base.Add(time.Duration(i)*time.Hour))
		seedMeeting(t, pool, otherID, fmt.Sprintf("bad-other-%d", i), base.Add(time.Duration(i)*time.Hour))
	}
	srv := newServer(t, pool)

	var otherPage v1.MeetingList
	getJSON(t, meetingsURL(srv, "it-meetings-bad-other", "", 1), http.StatusOK, &otherPage)
	if otherPage.NextCursor == nil {
		t.Fatal("other agency's first page has no cursor")
	}
	valid := *otherPage.NextCursor
	tampered := []byte(valid)
	tampered[len(tampered)/2] ^= 1

	for name, cursor := range map[string]string{
		"garbage":           "not-a-cursor",
		"tampered":          string(tampered),
		"another agency's":  valid,
		"truncated":         valid[:len(valid)-3],
		"trailing garbage":  valid + "x",
		"standard encoding": valid + "==",
	} {
		var body v1.Error
		getJSON(t, meetingsURL(srv, "it-meetings-bad", cursor, 1), http.StatusBadRequest, &body)
		if body.Code != "invalid_cursor" {
			t.Errorf("%s cursor: code = %q, want invalid_cursor", name, body.Code)
		}
	}

	for _, limit := range []string{"0", "201", "abc"} {
		var body v1.Error
		getJSON(t, srv.URL+"/v1/agencies/it-meetings-bad/meetings?limit="+limit, http.StatusBadRequest, &body)
		if body.Code != "invalid_parameter" {
			t.Errorf("limit %s: code = %q, want invalid_parameter", limit, body.Code)
		}
	}
}
