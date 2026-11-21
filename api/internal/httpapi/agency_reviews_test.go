package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const pendingPath = "/v1/admin/agency-submissions"

func approvePath(id uuid.UUID) string { return pendingPath + "/" + id.String() + "/approve" }

func rejectPath(id uuid.UUID) string { return pendingPath + "/" + id.String() + "/reject" }

func reviewHandler(role store.Role, flagOn bool, reviews *reviewCalls) (*Handler, *userCalls) {
	users := &userCalls{user: store.User{ID: uuid.New(), Role: role}}
	h := NewHandler(fakeStore{users: users, reviews: reviews}, slog.New(slog.DiscardHandler), "test",
		WithAuthenticator(goodAuth()), WithFlags(flagsOn(flagOn)))
	return h, users
}

func reviewRequest(t *testing.T, h *Handler, method, path, body, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	NewRouter(h, slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)
	return rec
}

func TestReviewRoutesAreAdminOnly(t *testing.T) {
	id := uuid.New()
	routes := []struct {
		method, path, body string
	}{
		{http.MethodGet, pendingPath, ""},
		{http.MethodPost, approvePath(id), `{"slug":"big-bear"}`},
		{http.MethodPost, rejectPath(id), ""},
		{http.MethodPost, pendingPath + "/not-a-uuid/approve", `{"slug":`},
		{http.MethodPost, pendingPath + "/not-a-uuid/reject", ""},
	}
	cases := []struct {
		name          string
		role          store.Role
		flagOn        bool
		authorization string
		wantStatus    int
		wantCode      string
	}{
		{name: "no token", role: store.RoleAdmin, flagOn: true, wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "invalid token", role: store.RoleAdmin, flagOn: true, authorization: "Bearer bad", wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "accounts off", role: store.RoleAdmin, authorization: "Bearer good", wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "user", role: store.RoleUser, flagOn: true, authorization: "Bearer good", wantStatus: http.StatusForbidden, wantCode: "forbidden"},
		{name: "impersonated admin", role: store.RoleAdmin, flagOn: true, authorization: "Bearer impersonating", wantStatus: http.StatusForbidden, wantCode: "forbidden"},
		{name: "token claiming admin", role: store.RoleUser, flagOn: true, authorization: "Bearer role-claim", wantStatus: http.StatusForbidden, wantCode: "forbidden"},
	}
	for _, route := range routes {
		for _, tt := range cases {
			t.Run(route.method+" "+route.path+" "+tt.name, func(t *testing.T) {
				reviews := &reviewCalls{}
				h, _ := reviewHandler(tt.role, tt.flagOn, reviews)
				rec := reviewRequest(t, h, route.method, route.path, route.body, tt.authorization)
				if rec.Code != tt.wantStatus || decodeError(t, rec).Code != tt.wantCode {
					t.Errorf("status %d, body %s; want %d %s", rec.Code, rec.Body, tt.wantStatus, tt.wantCode)
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
				}
				if len(reviews.calls) != 0 {
					t.Errorf("store called: %+v", reviews.calls)
				}
			})
		}
	}
}

func TestReviewHandlersRefuseWithoutTheGate(t *testing.T) {
	h, _ := reviewHandler(store.RoleAdmin, true, &reviewCalls{})
	ctx := context.Background()
	if res, _ := h.ListPendingAgencySubmissions(ctx, v1.ListPendingAgencySubmissionsRequestObject{}); fmt.Sprintf("%T", res) != "v1.ListPendingAgencySubmissions403JSONResponse" {
		t.Errorf("list without the gate = %T", res)
	}
	if res, _ := h.ApproveAgencySubmission(ctx, v1.ApproveAgencySubmissionRequestObject{Id: uuid.New(), Body: &v1.AgencyApprovalInput{Slug: "big-bear"}}); fmt.Sprintf("%T", res) != "v1.ApproveAgencySubmission403JSONResponse" {
		t.Errorf("approve without the gate = %T", res)
	}
	if res, _ := h.RejectAgencySubmission(ctx, v1.RejectAgencySubmissionRequestObject{Id: uuid.New()}); fmt.Sprintf("%T", res) != "v1.RejectAgencySubmission403JSONResponse" {
		t.Errorf("reject without the gate = %T", res)
	}
}

func TestListPendingAgencySubmissions(t *testing.T) {
	email := "ada@example.com"
	submitted := time.Date(2026, 11, 19, 10, 0, 0, 0, time.FixedZone("PST", -8*3600))
	first := store.PendingAgencySubmission{
		AgencyID: uuid.New(), Name: "Big Bear Transit", Website: "https://bigbear.example.org",
		SubmittedAt: submitted, SubmittedBy: uuid.New(), SubmitterEmail: &email,
	}
	second := store.PendingAgencySubmission{
		AgencyID: uuid.New(), Name: "No Email", Website: "https://noemail.example.org",
		SubmittedAt: submitted.Add(time.Hour), SubmittedBy: uuid.New(),
	}
	reviews := &reviewCalls{pending: []store.PendingAgencySubmission{first, second}}
	h, _ := reviewHandler(store.RoleAdmin, true, reviews)

	rec := reviewRequest(t, h, http.MethodGet, pendingPath, "", "Bearer good")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q, body %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body)
	}
	want := fmt.Sprintf(`{"submissions":[`+
		`{"id":%q,"name":"Big Bear Transit","submittedAt":"2026-11-19T18:00:00Z","submittedBy":{"email":"ada@example.com","id":%q},"website":"https://bigbear.example.org"},`+
		`{"id":%q,"name":"No Email","submittedAt":"2026-11-19T19:00:00Z","submittedBy":{"email":null,"id":%q},"website":"https://noemail.example.org"}]}`,
		first.AgencyID, first.SubmittedBy, second.AgencyID, second.SubmittedBy)
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body\n%s\nwant\n%s", got, want)
	}

	empty, _ := reviewHandler(store.RoleAdmin, true, &reviewCalls{})
	if rec := reviewRequest(t, empty, http.MethodGet, pendingPath, "", "Bearer good"); strings.TrimSpace(rec.Body.String()) != `{"submissions":[]}` {
		t.Errorf("empty list = %s", rec.Body)
	}

	failing, _ := reviewHandler(store.RoleAdmin, true, &reviewCalls{err: errors.New("connection refused")})
	if rec := reviewRequest(t, failing, http.MethodGet, pendingPath, "", "Bearer good"); rec.Code != http.StatusInternalServerError || decodeError(t, rec).Code != "internal_error" {
		t.Errorf("store error: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestApproveAgencySubmission(t *testing.T) {
	id := uuid.New()
	reviewed := store.ReviewedAgency{ID: id, Slug: "big-bear", Name: "Big Bear Transit", Website: "https://bigbear.example.org", Status: store.AgencyStatusLive}
	reviews := &reviewCalls{reviewed: reviewed}
	h, _ := reviewHandler(store.RoleAdmin, true, reviews)

	rec := reviewRequest(t, h, http.MethodPost, approvePath(id), `{"slug":"big-bear"}`, "Bearer good")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var body v1.ReviewedAgencySubmission
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body != (v1.ReviewedAgencySubmission{Id: id, Slug: "big-bear", Name: "Big Bear Transit", Website: "https://bigbear.example.org", Status: v1.ReviewedAgencySubmissionStatusLive}) {
		t.Errorf("body = %+v", body)
	}
	if len(reviews.calls) != 1 || reviews.calls[0] != (reviewCall{action: "approve", id: id, slug: "big-bear"}) {
		t.Errorf("store calls = %+v", reviews.calls)
	}
}

func TestApproveAgencySubmissionRefusesBadInput(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name, path, body string
		wantCode         string
	}{
		{name: "uppercase slug", path: approvePath(id), body: `{"slug":"Big-Bear"}`, wantCode: "invalid_slug"},
		{name: "submitted prefix", path: approvePath(id), body: `{"slug":"submitted-abc"}`, wantCode: "invalid_slug"},
		{name: "one character", path: approvePath(id), body: `{"slug":"a"}`, wantCode: "invalid_slug"},
		{name: "no slug", path: approvePath(id), body: `{}`, wantCode: "invalid_slug"},
		{name: "slug not a string", path: approvePath(id), body: `{"slug":12}`, wantCode: "invalid_body"},
		{name: "malformed body", path: approvePath(id), body: `{"slug":`, wantCode: "invalid_body"},
		{name: "id not a uuid", path: pendingPath + "/42/approve", body: `{"slug":"big-bear"}`, wantCode: "invalid_parameter"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			reviews := &reviewCalls{}
			h, _ := reviewHandler(store.RoleAdmin, true, reviews)
			rec := reviewRequest(t, h, http.MethodPost, tt.path, tt.body, "Bearer good")
			if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != tt.wantCode {
				t.Errorf("status %d, body %s; want 400 %s", rec.Code, rec.Body, tt.wantCode)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
			if len(reviews.calls) != 0 {
				t.Errorf("store called: %+v", reviews.calls)
			}
		})
	}
}

func TestReviewStoreErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		approve    bool
	}{
		{name: "approve unknown", err: store.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found", approve: true},
		{name: "approve reviewed", err: store.ErrNotPending, wantStatus: http.StatusConflict, wantCode: "not_pending", approve: true},
		{name: "approve taken slug", err: store.ErrSlugTaken, wantStatus: http.StatusConflict, wantCode: "slug_taken", approve: true},
		{name: "approve invalid slug", err: fmt.Errorf("wrapped: %w", store.ErrInvalidSlug), wantStatus: http.StatusBadRequest, wantCode: "invalid_slug", approve: true},
		{name: "approve database error", err: errors.New("connection refused"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error", approve: true},
		{name: "reject unknown", err: store.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "reject reviewed", err: store.ErrNotPending, wantStatus: http.StatusConflict, wantCode: "not_pending"},
		{name: "reject database error", err: errors.New("connection refused"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := reviewHandler(store.RoleAdmin, true, &reviewCalls{err: tt.err})
			path, body := rejectPath(uuid.New()), ""
			if tt.approve {
				path, body = approvePath(uuid.New()), `{"slug":"big-bear"}`
			}
			rec := reviewRequest(t, h, http.MethodPost, path, body, "Bearer good")
			if rec.Code != tt.wantStatus || decodeError(t, rec).Code != tt.wantCode {
				t.Errorf("status %d, body %s; want %d %s", rec.Code, rec.Body, tt.wantStatus, tt.wantCode)
			}
		})
	}
}

func TestRejectAgencySubmission(t *testing.T) {
	id := uuid.New()
	reviewed := store.ReviewedAgency{ID: id, Slug: store.SubmittedSlug(id), Name: "Big Bear Transit", Website: "https://bigbear.example.org", Status: store.AgencyStatusRejected}
	reviews := &reviewCalls{reviewed: reviewed}
	h, _ := reviewHandler(store.RoleAdmin, true, reviews)

	rec := reviewRequest(t, h, http.MethodPost, rejectPath(id), "", "Bearer good")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var body v1.ReviewedAgencySubmission
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != v1.ReviewedAgencySubmissionStatusRejected || body.Id != id {
		t.Errorf("body = %+v", body)
	}
	if len(reviews.calls) != 1 || reviews.calls[0] != (reviewCall{action: "reject", id: id}) {
		t.Errorf("store calls = %+v", reviews.calls)
	}
}

func TestReviewRoutesMethods(t *testing.T) {
	h, _ := reviewHandler(store.RoleAdmin, true, &reviewCalls{})
	for _, tt := range []struct{ method, path, allow string }{
		{http.MethodPost, pendingPath, "GET"},
		{http.MethodGet, approvePath(uuid.New()), "POST"},
		{http.MethodGet, rejectPath(uuid.New()), "POST"},
	} {
		rec := reviewRequest(t, h, tt.method, tt.path, "", "Bearer good")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != tt.allow {
			t.Errorf("%s %s: status %d, Allow %q", tt.method, tt.path, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

func TestReviewOperationsArePrivate(t *testing.T) {
	for _, op := range []string{"ListPendingAgencySubmissions", "ApproveAgencySubmission", "RejectAgencySubmission"} {
		if !privateOperations[op] {
			t.Errorf("%s is not in privateOperations", op)
		}
		if _, ok := adminOperations[op]; !ok {
			t.Errorf("%s is not in adminOperations", op)
		}
	}
}
