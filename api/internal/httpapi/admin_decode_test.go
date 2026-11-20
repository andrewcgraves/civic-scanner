package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

func TestAdminGateRunsBeforeDecoding(t *testing.T) {
	admin := store.User{ID: uuid.New(), Role: store.RoleAdmin}
	user := store.User{ID: uuid.New(), Role: store.RoleUser}
	good := "/v1/test-admin/" + uuid.NewString()
	tests := []struct {
		name          string
		user          store.User
		path          string
		body          string
		authorization string
		wantStatus    int
		wantCode      string
	}{
		{"malformed body, no token", admin, good, `{"note":`, "", http.StatusUnauthorized, "unauthorized"},
		{"malformed body, invalid token", admin, good, `{"note":`, "Bearer bad", http.StatusUnauthorized, "unauthorized"},
		{"malformed body, not an admin", user, good, `{"note":`, "Bearer good", http.StatusForbidden, "forbidden"},
		{"malformed body, admin", admin, good, `{"note":`, "Bearer good", http.StatusBadRequest, "invalid_body"},
		{"wrongly typed body, admin", admin, good, `{"note":7}`, "Bearer good", http.StatusBadRequest, "invalid_body"},
		{"bad path parameter, no token", admin, "/v1/test-admin/not-a-uuid", `{}`, "", http.StatusUnauthorized, "unauthorized"},
		{"bad path parameter, not an admin", user, "/v1/test-admin/not-a-uuid", `{}`, "Bearer good", http.StatusForbidden, "forbidden"},
		{"bad path parameter, admin", admin, "/v1/test-admin/not-a-uuid", `{}`, "Bearer good", http.StatusBadRequest, "invalid_parameter"},
		{"escaped path, no token", admin, "/v1/test-admin/a%2Fb", `{}`, "", http.StatusUnauthorized, "unauthorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := &userCalls{user: tt.user}
			h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
			probe := &adminProbe{}
			rec := adminRequestTo(t, adminTestRouter(h, probe, testAdminRoutes), tt.path, tt.body, tt.authorization)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if body := decodeError(t, rec); body.Code != tt.wantCode || body.Message == "" {
				t.Errorf("body = %+v, want code %q with a message", body, tt.wantCode)
			}
			if probe.calls != 0 {
				t.Errorf("handler ran %d times", probe.calls)
			}
		})
	}
}

func TestAdminGateServesAValidRequest(t *testing.T) {
	admin := store.User{ID: uuid.New(), Role: store.RoleAdmin}
	h := NewHandler(fakeStore{users: &userCalls{user: admin}}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
	probe := &adminProbe{}
	id := uuid.NewString()
	rec := adminRequestTo(t, adminTestRouter(h, probe, testAdminRoutes), "/v1/test-admin/"+id, `{"note":"approve"}`, "Bearer good")

	if rec.Code != http.StatusOK || probe.calls != 1 || !probe.ok || probe.admin.ID != admin.ID || probe.note != "approve" {
		t.Fatalf("status %d, body %s, probe %+v", rec.Code, rec.Body, probe)
	}
	if !strings.Contains(rec.Body.String(), id) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("body %s, Cache-Control %q", rec.Body, rec.Header().Get("Cache-Control"))
	}
}

func TestUngatedRoutesDecodeWithoutAuth(t *testing.T) {
	calls := &userCalls{}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
	probe := &adminProbe{}
	rec := adminRequestTo(t, adminTestRouter(h, probe, testAdminRoutes), "/v1/test-open/"+uuid.NewString(), `{"note":`, "")

	if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "invalid_body" || len(calls.inputs) != 0 {
		t.Errorf("status %d, body %s, lookups %d; want 400 invalid_body without a user lookup", rec.Code, rec.Body, len(calls.inputs))
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("Cache-Control = %q on an ungated route, want none", got)
	}
}

func TestResponseFailedAnswersInternalError(t *testing.T) {
	rec := httptest.NewRecorder()
	responseFailed(slog.New(slog.DiscardHandler))(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil), errors.New("unexpected response type"))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, Content-Type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if body := decodeError(t, rec); body != internalError {
		t.Errorf("body = %+v, want %+v", body, internalError)
	}
}
