package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

func flagsSet(on ...string) func(context.Context, string) bool {
	return func(_ context.Context, flag string) bool { return slices.Contains(on, flag) }
}

var submissionFlags = flagsSet(FlagAccounts, FlagAgencySubmissions)

type submissionFixture struct {
	users       *userCalls
	submissions *submissionCalls
	handler     *Handler
}

func newSubmissionFixture(a *fakeAuth, flags func(context.Context, string) bool) submissionFixture {
	f := submissionFixture{
		users:       &userCalls{user: store.User{ID: uuid.New(), WorkOSUserID: "user_01"}},
		submissions: &submissionCalls{},
	}
	if a == nil {
		a = &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01", Email: "ada@example.com"}}}
	}
	f.handler = NewHandler(fakeStore{users: f.users, submissions: f.submissions}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flags))
	return f
}

func post(t *testing.T, h *Handler, path, authorization, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := NewRouter(h, slog.New(slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeValidation(t *testing.T, rec *httptest.ResponseRecorder) v1.ValidationError {
	t.Helper()
	var body v1.ValidationError
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body
}

const goodSubmission = `{"name":"  Big Bear   Transit ","website":"WWW.BigBearTransit.org/"}`

func TestCreateAgencySubmission(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	submitted := time.Date(2026, 11, 19, 12, 0, 0, 0, time.FixedZone("PST", -8*3600))
	f.submissions.result = store.AgencySubmission{
		AgencyID: uuid.New(), Slug: "submitted-x", Name: "Big Bear Transit", Website: "https://www.bigbeartransit.org",
		SubmittedBy: f.users.user.ID, SubmittedAt: submitted,
	}

	rec := post(t, f.handler, "/v1/agency-submissions", "Bearer good", goodSubmission)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var body v1.AgencySubmission
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Id != f.submissions.result.AgencyID || body.Name != "Big Bear Transit" || body.Website != "https://www.bigbeartransit.org" ||
		body.Status != v1.AgencySubmissionStatusPending || !body.SubmittedAt.Equal(submitted) || body.SubmittedAt.Location() != time.UTC {
		t.Errorf("body = %+v", body)
	}
	if strings.Contains(rec.Body.String(), "submitted-x") || strings.Contains(rec.Body.String(), "user_01") {
		t.Errorf("response exposes the slug or the WorkOS id: %s", rec.Body)
	}
	want := store.AgencySubmissionInput{UserID: f.users.user.ID, Name: "Big Bear Transit", Website: "https://www.bigbeartransit.org", WebsiteKey: "bigbeartransit.org"}
	if len(f.submissions.inputs) != 1 || f.submissions.inputs[0] != want {
		t.Errorf("store inputs = %+v, want %+v", f.submissions.inputs, want)
	}
	if len(f.users.inputs) != 1 || f.users.inputs[0].WorkOSUserID != "user_01" {
		t.Errorf("EnsureUser inputs = %+v", f.users.inputs)
	}
}

func TestCreateAgencySubmissionChecksTheTokenFirst(t *testing.T) {
	tests := []struct {
		name          string
		auth          *fakeAuth
		authorization string
		status        int
		code          string
	}{
		{"no token", nil, "", http.StatusUnauthorized, "unauthorized"},
		{"invalid token", nil, "Bearer bad", http.StatusUnauthorized, "unauthorized"},
		{"not configured", &fakeAuth{unconfigured: true}, "Bearer good", http.StatusServiceUnavailable, "auth_not_configured"},
		{"unavailable", &fakeAuth{err: auth.ErrUnavailable}, "Bearer good", http.StatusServiceUnavailable, "auth_unavailable"},
	}
	bodies := map[string]string{
		"valid body":     goodSubmission,
		"invalid fields": `{"name":"","website":"localhost"}`,
		"malformed body": `{"name":`,
		"oversized body": `{"name":"` + strings.Repeat("a", maxRequestBodyBytes) + `"}`,
	}
	for _, tt := range tests {
		for bodyName, body := range bodies {
			for flagName, flags := range map[string]func(context.Context, string) bool{"flags on": submissionFlags, "flags off": flagsSet()} {
				t.Run(tt.name+"/"+bodyName+"/"+flagName, func(t *testing.T) {
					f := newSubmissionFixture(tt.auth, flags)
					rec := post(t, f.handler, "/v1/agency-submissions", tt.authorization, body)
					if rec.Code != tt.status {
						t.Fatalf("status = %d, want %d, body %s", rec.Code, tt.status, rec.Body)
					}
					if got := decodeError(t, rec); got.Code != tt.code {
						t.Errorf("code = %q, want %q", got.Code, tt.code)
					}
					if got := rec.Header().Get("Cache-Control"); got != "no-store" {
						t.Errorf("Cache-Control = %q, want no-store", got)
					}
					if tt.status == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
						t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
					}
					if len(f.users.inputs) != 0 || len(f.submissions.inputs) != 0 {
						t.Errorf("store was called: users %+v, submissions %+v", f.users.inputs, f.submissions.inputs)
					}
				})
			}
		}
	}
}

func TestCreateAgencySubmissionBehindFlags(t *testing.T) {
	for name, flags := range map[string]func(context.Context, string) bool{
		"both off":                flagsSet(),
		"only accounts":           flagsSet(FlagAccounts),
		"only agency-submissions": flagsSet(FlagAgencySubmissions),
	} {
		for _, body := range []string{goodSubmission, `not json`, `{"name":"x"}`} {
			t.Run(name+"/"+body, func(t *testing.T) {
				f := newSubmissionFixture(nil, flags)
				rec := post(t, f.handler, "/v1/agency-submissions", "Bearer good", body)
				if rec.Code != http.StatusNotFound || decodeError(t, rec).Code != "not_found" {
					t.Fatalf("status = %d, body %s, want 404 not_found", rec.Code, rec.Body)
				}
				if len(f.users.inputs) != 0 || len(f.submissions.inputs) != 0 {
					t.Errorf("store was called: users %+v, submissions %+v", f.users.inputs, f.submissions.inputs)
				}
			})
		}
	}
}

func TestCreateAgencySubmissionRejectsBadBodies(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		code       string
	}{
		{"malformed", `{"name":`, http.StatusBadRequest, "invalid_body"},
		{"array", `[]`, http.StatusBadRequest, "invalid_body"},
		{"number name", `{"name":1,"website":"metro.net"}`, http.StatusBadRequest, "invalid_body"},
		{"empty", ``, http.StatusBadRequest, "invalid_body"},
		{"too large", `{"name":"` + strings.Repeat("a", maxRequestBodyBytes) + `","website":"metro.net"}`, http.StatusRequestEntityTooLarge, "body_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSubmissionFixture(nil, submissionFlags)
			rec := post(t, f.handler, "/v1/agency-submissions", "Bearer good", tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tt.status, rec.Body)
			}
			body := decodeValidation(t, rec)
			if body.Code != tt.code || body.Message == "" || body.Fields != nil {
				t.Errorf("body = %+v, want code %s without fields", body, tt.code)
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("headers = %v", rec.Header())
			}
			if len(f.users.inputs) != 0 || len(f.submissions.inputs) != 0 {
				t.Errorf("store was called")
			}
		})
	}
}

func TestCreateAgencySubmissionReportsEachInvalidField(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	zeroWidthSpace := string(rune(0x200b))
	rec := post(t, f.handler, "/v1/agency-submissions", "Bearer good", `{"name":"Metro`+zeroWidthSpace+`","website":"http://127.0.0.1/"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	body := decodeValidation(t, rec)
	if body.Code != "invalid_submission" || body.Fields == nil {
		t.Fatalf("body = %+v", body)
	}
	fields := *body.Fields
	if len(fields) != 2 || fields[0].Field != v1.FieldErrorFieldName || fields[0].Code != "invalid_characters" ||
		fields[1].Field != v1.FieldErrorFieldWebsite || fields[1].Code != "ip_address" || fields[1].Message == "" {
		t.Errorf("fields = %+v", fields)
	}
	if len(f.users.inputs) != 0 || len(f.submissions.inputs) != 0 {
		t.Errorf("store was called for an invalid submission")
	}

	rec = post(t, f.handler, "/v1/agency-submissions", "Bearer good", `{"website":"metro.net"}`)
	if body := decodeValidation(t, rec); rec.Code != http.StatusBadRequest || body.Fields == nil || len(*body.Fields) != 1 || (*body.Fields)[0].Code != "required" {
		t.Errorf("missing name: %d %s", rec.Code, rec.Body)
	}
}

func TestCreateAgencySubmissionStoreOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		userErr  error
		storeErr error
		status   int
		code     string
	}{
		{"on file", nil, store.ErrWebsiteOnFile, http.StatusConflict, "already_on_file"},
		{"limit", nil, store.ErrSubmissionLimit, http.StatusTooManyRequests, "submission_limit_reached"},
		{"store error", nil, errors.New("boom"), http.StatusInternalServerError, "internal_error"},
		{"user error", errors.New("boom"), nil, http.StatusInternalServerError, "internal_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSubmissionFixture(nil, submissionFlags)
			f.users.err = tt.userErr
			f.submissions.err = tt.storeErr
			rec := post(t, f.handler, "/v1/agency-submissions", "Bearer good", goodSubmission)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tt.status, rec.Body)
			}
			body := decodeError(t, rec)
			if body.Code != tt.code || strings.Contains(body.Message, "bigbear") || strings.Contains(body.Message, "boom") {
				t.Errorf("body = %+v", body)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestCreateAgencySubmissionWithoutBody(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	ctx := context.WithValue(t.Context(), authContextKey{}, authResult{identity: auth.Identity{UserID: "user_01"}})
	resp, err := f.handler.CreateAgencySubmission(ctx, v1.CreateAgencySubmissionRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.(v1.CreateAgencySubmission400JSONResponse); !ok {
		t.Errorf("response = %#v, want 400", resp)
	}
}

func TestAgencySubmissionsOnlyAcceptsPost(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	rec := request(t, f.handler, "/v1/agency-submissions", "Bearer good")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET: status %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

type brokenResponseWriter struct {
	header http.Header
}

func (w *brokenResponseWriter) Header() http.Header       { return w.header }
func (w *brokenResponseWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }
func (w *brokenResponseWriter) WriteHeader(int)           {}

func TestRequestBodyErrorWithAFailingWriter(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	req := httptest.NewRequest(http.MethodPost, "/v1/agency-submissions", nil)
	w := &brokenResponseWriter{header: http.Header{}}
	f.handler.requestBodyError(w, req, errors.New("bad"))
	if got := w.header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
