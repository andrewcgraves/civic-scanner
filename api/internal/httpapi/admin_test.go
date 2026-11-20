package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const testAdminOperation = "TestAdminOperation"

type adminProbe struct {
	calls int
	admin store.User
	ok    bool
}

func adminTestRouter(h *Handler, probe *adminProbe, operations map[string]bool) http.Handler {
	inner := func(ctx context.Context, w http.ResponseWriter, _ *http.Request, _ any) (any, error) {
		probe.calls++
		probe.admin, probe.ok = adminFrom(ctx)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"admin": probe.admin.ID.String()})
		return nil, nil
	}
	gated := h.adminOnly(operations)(inner, testAdminOperation)
	r := chi.NewRouter()
	r.Use(authenticate(h.auth, slog.New(slog.DiscardHandler)))
	r.Get("/admin-test", func(w http.ResponseWriter, req *http.Request) {
		_, _ = gated(req.Context(), w, req, nil)
	})
	return r
}

func adminRequest(t *testing.T, router http.Handler, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin-test", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func goodAuth() *fakeAuth {
	return &fakeAuth{tokens: map[string]auth.Identity{
		"good":          {UserID: "user_01", Email: "ada@example.com"},
		"impersonating": {UserID: "user_01", Actor: "workos_admin_01"},
	}}
}

func TestAdminOnlyMatrix(t *testing.T) {
	adminID := uuid.New()
	tests := []struct {
		name          string
		auth          *fakeAuth
		flagOn        bool
		user          store.User
		storeErr      error
		authorization string
		wantStatus    int
		wantCode      string
		wantLookup    bool
	}{
		{name: "no token", auth: goodAuth(), flagOn: true, wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "invalid token", auth: goodAuth(), flagOn: true, authorization: "Bearer bad", wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "malformed header", auth: goodAuth(), flagOn: true, authorization: "Basic dXNlcjpwYXNz", wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "no token with accounts off", auth: goodAuth(), wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "not configured", auth: &fakeAuth{unconfigured: true}, flagOn: true, authorization: "Bearer good", wantStatus: http.StatusServiceUnavailable, wantCode: "auth_not_configured"},
		{name: "keys unavailable", auth: &fakeAuth{err: fmt.Errorf("%w: down", auth.ErrUnavailable)}, flagOn: true, authorization: "Bearer good", wantStatus: http.StatusServiceUnavailable, wantCode: "auth_unavailable"},
		{name: "accounts off", auth: goodAuth(), user: store.User{ID: adminID, Role: store.RoleAdmin}, authorization: "Bearer good", wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "user role", auth: goodAuth(), flagOn: true, user: store.User{ID: uuid.New(), Role: store.RoleUser}, authorization: "Bearer good", wantStatus: http.StatusForbidden, wantCode: "forbidden", wantLookup: true},
		{name: "missing role", auth: goodAuth(), flagOn: true, user: store.User{ID: uuid.New()}, authorization: "Bearer good", wantStatus: http.StatusForbidden, wantCode: "forbidden", wantLookup: true},
		{name: "unknown role", auth: goodAuth(), flagOn: true, user: store.User{ID: uuid.New(), Role: "superuser"}, authorization: "Bearer good", wantStatus: http.StatusForbidden, wantCode: "forbidden", wantLookup: true},
		{name: "role in another case", auth: goodAuth(), flagOn: true, user: store.User{ID: uuid.New(), Role: "Admin"}, authorization: "Bearer good", wantStatus: http.StatusForbidden, wantCode: "forbidden", wantLookup: true},
		{name: "impersonated admin", auth: goodAuth(), flagOn: true, user: store.User{ID: adminID, Role: store.RoleAdmin}, authorization: "Bearer impersonating", wantStatus: http.StatusForbidden, wantCode: "forbidden", wantLookup: true},
		{name: "user lookup fails", auth: goodAuth(), flagOn: true, user: store.User{ID: adminID, Role: store.RoleAdmin}, storeErr: errors.New("connection refused"), authorization: "Bearer good", wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantLookup: true},
		{name: "admin", auth: goodAuth(), flagOn: true, user: store.User{ID: adminID, Role: store.RoleAdmin}, authorization: "Bearer good", wantStatus: http.StatusOK, wantLookup: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := &userCalls{user: tt.user, err: tt.storeErr}
			h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(tt.auth), WithFlags(flagsOn(tt.flagOn)))
			probe := &adminProbe{}
			rec := adminRequest(t, adminTestRouter(h, probe, map[string]bool{testAdminOperation: true}), tt.authorization)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if (len(calls.inputs) > 0) != tt.wantLookup {
				t.Errorf("EnsureUser calls = %+v, want lookup %v", calls.inputs, tt.wantLookup)
			}
			if tt.wantStatus == http.StatusOK {
				if probe.calls != 1 || !probe.ok || probe.admin.ID != adminID {
					t.Errorf("handler saw %+v (calls %d, ok %v), want admin %s", probe.admin, probe.calls, probe.ok, adminID)
				}
				return
			}
			if probe.calls != 0 {
				t.Errorf("handler ran %d times for a refused request", probe.calls)
			}
			if body := decodeError(t, rec); body.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Code, tt.wantCode)
			}
			wantChallenge := ""
			if tt.wantStatus == http.StatusUnauthorized {
				wantChallenge = "Bearer"
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != wantChallenge {
				t.Errorf("WWW-Authenticate = %q, want %q", got, wantChallenge)
			}
		})
	}
}

func TestAdminOnlyReadsTheRoleOnEveryRequest(t *testing.T) {
	calls := &userCalls{user: store.User{ID: uuid.New(), Role: store.RoleUser}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
	probe := &adminProbe{}
	router := adminTestRouter(h, probe, map[string]bool{testAdminOperation: true})

	steps := []struct {
		role store.Role
		want int
	}{
		{store.RoleUser, http.StatusForbidden},
		{store.RoleAdmin, http.StatusOK},
		{store.RoleAdmin, http.StatusOK},
		{store.RoleUser, http.StatusForbidden},
	}
	for i, step := range steps {
		calls.user.Role = step.role
		if rec := adminRequest(t, router, "Bearer good"); rec.Code != step.want {
			t.Errorf("request %d with role %q: status = %d, want %d", i, step.role, rec.Code, step.want)
		}
	}
	if len(calls.inputs) != len(steps) {
		t.Errorf("EnsureUser calls = %d, want one per request (%d)", len(calls.inputs), len(steps))
	}
}

func TestAdminOnlyLeavesOtherOperationsAlone(t *testing.T) {
	calls := &userCalls{}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
	probe := &adminProbe{}
	rec := adminRequest(t, adminTestRouter(h, probe, map[string]bool{"SomethingElse": true}), "")

	if rec.Code != http.StatusOK || probe.calls != 1 || len(calls.inputs) != 0 {
		t.Fatalf("status %d, handler calls %d, lookups %d; want the handler untouched", rec.Code, probe.calls, len(calls.inputs))
	}
	if probe.ok {
		t.Error("adminFrom reported an admin on an ungated operation")
	}
}

func TestAdminFromFailsClosed(t *testing.T) {
	ctx := context.Background()
	if _, ok := adminFrom(ctx); ok {
		t.Error("adminFrom on a bare context reported an admin")
	}
	for _, role := range []store.Role{"", store.RoleUser, "superuser"} {
		if _, ok := adminFrom(context.WithValue(ctx, adminContextKey{}, store.User{Role: role})); ok {
			t.Errorf("adminFrom with role %q reported an admin", role)
		}
	}
	if _, ok := adminFrom(context.WithValue(ctx, adminContextKey{}, "admin")); ok {
		t.Error("adminFrom accepted a value that is not a user")
	}
	admin := store.User{ID: uuid.New(), Role: store.RoleAdmin}
	if got, ok := adminFrom(context.WithValue(ctx, adminContextKey{}, admin)); !ok || got.ID != admin.ID {
		t.Errorf("adminFrom = (%+v, %v), want the admin", got, ok)
	}
}

func TestRouterRunsTheAdminGate(t *testing.T) {
	adminOperations["GetMe"] = true
	t.Cleanup(func() { delete(adminOperations, "GetMe") })

	calls := &userCalls{user: store.User{ID: uuid.New(), Role: store.RoleUser}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))

	if rec := request(t, h, "/v1/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	if rec := request(t, h, "/v1/me", "Bearer good"); rec.Code != http.StatusForbidden || decodeError(t, rec).Code != "forbidden" {
		t.Errorf("user: status = %d, body %s, want 403 forbidden", rec.Code, rec.Body)
	}
	calls.user.Role = store.RoleAdmin
	rec := request(t, h, "/v1/me", "Bearer good")
	var me v1.User
	if err := json.Unmarshal(rec.Body.Bytes(), &me); rec.Code != http.StatusOK || err != nil || me.Role != v1.UserRoleAdmin {
		t.Errorf("admin: status = %d, body %s, want 200 with role admin", rec.Code, rec.Body)
	}
}

func TestGetMeRole(t *testing.T) {
	for _, tt := range []struct {
		stored store.Role
		want   v1.UserRole
	}{
		{store.RoleUser, v1.UserRoleUser},
		{store.RoleAdmin, v1.UserRoleAdmin},
		{"", v1.UserRoleUser},
		{"superuser", v1.UserRoleUser},
	} {
		t.Run(string(tt.stored), func(t *testing.T) {
			calls := &userCalls{user: store.User{ID: uuid.New(), Role: tt.stored}}
			h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
			rec := request(t, h, "/v1/me", "Bearer good")
			var me v1.User
			if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("status %d, body %s, err %v", rec.Code, rec.Body, err)
			}
			if me.Role != tt.want {
				t.Errorf("role = %q, want %q", me.Role, tt.want)
			}
		})
	}
}
