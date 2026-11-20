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

	"github.com/go-chi/chi/v5"
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

func postJSON(t *testing.T, h *Handler, path, authorization, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postAs(t, h, path, authorization, "application/json", body)
}

func postAs(t *testing.T, h *Handler, path, authorization, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := NewRouter(h, slog.New(slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
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

	rec := postJSON(t, f.handler, "/v1/agency-submissions", "Bearer good", goodSubmission)
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
					rec := postJSON(t, f.handler, "/v1/agency-submissions", tt.authorization, body)
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
				rec := postJSON(t, f.handler, "/v1/agency-submissions", "Bearer good", body)
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
	const jsonType = "application/json"
	tests := []struct {
		name, contentType, body string
		status                  int
		code, message           string
	}{
		{"malformed", jsonType, `{"name":`, http.StatusBadRequest, "invalid_body", errNotObject.Error()},
		{"array", jsonType, `[]`, http.StatusBadRequest, "invalid_body", errNotObject.Error()},
		{"string", jsonType, `"x"`, http.StatusBadRequest, "invalid_body", errNotObject.Error()},
		{"number name", jsonType, `{"name":1,"website":"metro.net"}`, http.StatusBadRequest, "invalid_body", errFieldNotString.Error()},
		{"null website", jsonType, `{"name":"Metro","website":null}`, http.StatusBadRequest, "invalid_body", errFieldNotString.Error()},
		{"object name", jsonType, `{"name":{"x":1},"website":"metro.net"}`, http.StatusBadRequest, "invalid_body", errFieldNotString.Error()},
		{"empty", jsonType, ``, http.StatusBadRequest, "invalid_body", errNotObject.Error()},
		{"duplicate key", jsonType, `{"name":"Metro","website":"metro.net","name":"Other"}`, http.StatusBadRequest, "invalid_body", errDuplicateField.Error()},
		{"unknown field", jsonType, `{"name":"Metro","website":"metro.net","status":"live"}`, http.StatusBadRequest, "invalid_body", errUnknownField.Error()},
		{"field in another case", jsonType, `{"Name":"Metro","website":"metro.net"}`, http.StatusBadRequest, "invalid_body", errUnknownField.Error()},
		{"trailing object", jsonType, `{"name":"Metro","website":"metro.net"}{"name":"x"}`, http.StatusBadRequest, "invalid_body", errTrailingData.Error()},
		{"trailing garbage", jsonType, `{"name":"Metro","website":"metro.net"} x`, http.StatusBadRequest, "invalid_body", errTrailingData.Error()},
		{"number key", jsonType, `{1:"x"}`, http.StatusBadRequest, "invalid_body", errNotObject.Error()},
		{"missing value", jsonType, `{"name":}`, http.StatusBadRequest, "invalid_body", errNotObject.Error()},
		{"unclosed object", jsonType, `{"name":"Metro","website":"metro.net"`, http.StatusBadRequest, "invalid_body", errNotObject.Error()},
		{"too large", jsonType, `{"name":"` + strings.Repeat("a", maxRequestBodyBytes) + `","website":"metro.net"}`, http.StatusRequestEntityTooLarge, "body_too_large", errBodyTooLarge.Error()},
		{"no content type", "", goodSubmission, http.StatusUnsupportedMediaType, "unsupported_media_type", errNotJSON.Error()},
		{"form content type", "application/x-www-form-urlencoded", goodSubmission, http.StatusUnsupportedMediaType, "unsupported_media_type", errNotJSON.Error()},
		{"text content type", "text/plain", goodSubmission, http.StatusUnsupportedMediaType, "unsupported_media_type", errNotJSON.Error()},
		{"latin-1 charset", "application/json; charset=iso-8859-1", goodSubmission, http.StatusUnsupportedMediaType, "unsupported_media_type", errNotJSON.Error()},
		{"unparsable content type", "application/json; =", goodSubmission, http.StatusUnsupportedMediaType, "unsupported_media_type", errNotJSON.Error()},
		{"malformed and not JSON", "text/plain", `{"name":`, http.StatusUnsupportedMediaType, "unsupported_media_type", errNotJSON.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSubmissionFixture(nil, submissionFlags)
			rec := postAs(t, f.handler, "/v1/agency-submissions", "Bearer good", tt.contentType, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tt.status, rec.Body)
			}
			body := decodeValidation(t, rec)
			if body.Code != tt.code || body.Message != tt.message || body.Fields != nil {
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
	rec := postJSON(t, f.handler, "/v1/agency-submissions", "Bearer good", `{"name":"Metro`+zeroWidthSpace+`","website":"http://127.0.0.1/"}`)
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

	rec = postJSON(t, f.handler, "/v1/agency-submissions", "Bearer good", `{"website":"metro.net"}`)
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
			rec := postJSON(t, f.handler, "/v1/agency-submissions", "Bearer good", goodSubmission)
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

func signedInContext(t *testing.T) context.Context {
	return context.WithValue(t.Context(), authContextKey{}, authResult{identity: auth.Identity{UserID: "user_01"}})
}

func TestCreateAgencySubmissionWithoutACapturedBody(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	resp, err := f.handler.CreateAgencySubmission(signedInContext(t), v1.CreateAgencySubmissionRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.(v1.CreateAgencySubmission415JSONResponse); !ok {
		t.Errorf("response = %#v, want 415", resp)
	}
}

func TestCreateAgencySubmissionAcceptsJSONMediaTypeVariants(t *testing.T) {
	for _, contentType := range []string{"application/json; charset=utf-8", "Application/JSON; Charset=UTF-8", "application/json;foo=bar"} {
		f := newSubmissionFixture(nil, submissionFlags)
		rec := postAs(t, f.handler, "/v1/agency-submissions", "Bearer good", contentType, goodSubmission)
		if rec.Code != http.StatusCreated {
			t.Errorf("%q: status %d, body %s", contentType, rec.Code, rec.Body)
		}
	}
}

func TestCreateAgencySubmissionRefusesReplacementCharacters(t *testing.T) {
	tests := map[string]struct {
		body, field, code string
	}{
		"invalid bytes in the name":  {"{\"name\":\"Big \xff\xfe\",\"website\":\"bigbear.org\"}", "name", "invalid_characters"},
		"escaped U+FFFD in the name": {`{"name":"Big \ufffd","website":"bigbear.org"}`, "name", "invalid_characters"},
		"invalid bytes in the site":  {"{\"name\":\"Big Bear\",\"website\":\"bigbear\xff.org\"}", "website", "invalid_url"},
		"escaped U+FFFD in the site": {`{"name":"Big Bear","website":"bigbear.org/\ufffd"}`, "website", "invalid_url"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := newSubmissionFixture(nil, submissionFlags)
			rec := postJSON(t, f.handler, "/v1/agency-submissions", "Bearer good", tt.body)
			body := decodeValidation(t, rec)
			if rec.Code != http.StatusBadRequest || body.Fields == nil || len(*body.Fields) != 1 ||
				string((*body.Fields)[0].Field) != tt.field || (*body.Fields)[0].Code != tt.code {
				t.Errorf("status %d, body %s", rec.Code, rec.Body)
			}
			if len(f.submissions.inputs) != 0 {
				t.Errorf("stored %+v", f.submissions.inputs)
			}
		})
	}
}

type erroringBody struct{}

func (erroringBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestCreateAgencySubmissionWhenTheBodyCannotBeRead(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	router := NewRouter(f.handler, slog.New(slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodPost, "/v1/agency-submissions", erroringBody{})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if body := decodeValidation(t, rec); rec.Code != http.StatusBadRequest || body.Code != "invalid_body" {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func onSubmissionRoute(ctx context.Context) context.Context {
	routeContext := chi.NewRouteContext()
	routeContext.RoutePatterns = []string{agencySubmissionsPattern}
	return context.WithValue(ctx, chi.RouteCtxKey, routeContext)
}

func TestRequestBodyErrorWithABodyTheStrictParserAccepts(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	ctx := onSubmissionRoute(context.WithValue(signedInContext(t), bodyContextKey{}, capturedBody{contentType: "application/json", data: []byte(goodSubmission)}))
	rec := httptest.NewRecorder()
	f.handler.requestBodyError(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/agency-submissions", nil), errors.New("decoder disagreed"))
	if body := decodeValidation(t, rec); rec.Code != http.StatusBadRequest || body.Code != "invalid_body" {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
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
	req := httptest.NewRequestWithContext(onSubmissionRoute(t.Context()), http.MethodPost, "/v1/agency-submissions", nil)
	w := &brokenResponseWriter{header: http.Header{}}
	f.handler.requestBodyError(w, req, errors.New("bad"))
	if got := w.header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestRequestBodyErrorOnOtherRoutesUsesTheSharedInvalidBody(t *testing.T) {
	f := newSubmissionFixture(nil, submissionFlags)
	for name, ctx := range map[string]context.Context{"no route": t.Context(), "another route": func() context.Context {
		routeContext := chi.NewRouteContext()
		routeContext.RoutePatterns = []string{"/v1/test-open/{id}"}
		return context.WithValue(t.Context(), chi.RouteCtxKey, routeContext)
	}()} {
		rec := httptest.NewRecorder()
		f.handler.requestBodyError(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/x", nil), errors.New("can't decode JSON body: boom"))
		if body := decodeError(t, rec); rec.Code != http.StatusBadRequest || body.Code != "invalid_body" || body.Message != "can't decode JSON body: boom" {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
}

func TestAgencySubmissionIsPrivateAndNotAdminOnly(t *testing.T) {
	if _, ok := adminOperations["CreateAgencySubmission"]; ok {
		t.Error("CreateAgencySubmission is admin-only")
	}
	if !privateOperations["CreateAgencySubmission"] {
		t.Error("CreateAgencySubmission is not in privateOperations")
	}
}
