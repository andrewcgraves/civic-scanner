package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-feature/go-sdk/openfeature"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

type fakeAuth struct {
	unconfigured bool
	tokens       map[string]auth.Identity
	err          error
	seen         []string
}

func (f *fakeAuth) Configured() bool { return !f.unconfigured }

func (f *fakeAuth) Verify(_ context.Context, token string) (auth.Identity, error) {
	f.seen = append(f.seen, token)
	if f.err != nil {
		return auth.Identity{}, f.err
	}
	id, ok := f.tokens[token]
	if !ok {
		return auth.Identity{}, fmt.Errorf("%w: unknown", auth.ErrInvalidToken)
	}
	return id, nil
}

func flagsOn(on bool) func(context.Context, string) bool {
	return func(_ context.Context, flag string) bool { return on && flag == FlagAccounts }
}

func request(t *testing.T, h *Handler, path, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	router := NewRouter(h, slog.New(slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) v1.Error {
	t.Helper()
	var body v1.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestGetMeReturnsTheSignedInUser(t *testing.T) {
	email := "ada@example.com"
	created := time.Date(2026, 11, 18, 4, 0, 0, 0, time.FixedZone("PST", -8*3600))
	user := store.User{ID: uuid.New(), WorkOSUserID: "user_01", Email: &email, CreatedAt: created}
	calls := &userCalls{user: user}
	a := &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01", Email: email}}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flagsOn(true)))

	rec := request(t, h, "/v1/me", "Bearer good")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body v1.User
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Id != user.ID || body.Email == nil || *body.Email != email || !body.CreatedAt.Equal(created) || body.CreatedAt.Location() != time.UTC {
		t.Errorf("body = %+v", body)
	}
	if want := []store.UserInput{{WorkOSUserID: "user_01", Email: email}}; len(calls.inputs) != 1 || calls.inputs[0] != want[0] {
		t.Errorf("EnsureUser inputs = %+v, want %+v", calls.inputs, want)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if strings.Contains(rec.Body.String(), "user_01") {
		t.Errorf("response exposes the WorkOS user id: %s", rec.Body)
	}
}

func TestGetMeNullEmail(t *testing.T) {
	calls := &userCalls{user: store.User{ID: uuid.New(), WorkOSUserID: "user_01"}}
	a := &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01"}}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flagsOn(true)))

	rec := request(t, h, "/v1/me", "Bearer good")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":null`) {
		t.Errorf("status %d, body %s, want 200 with a null email", rec.Code, rec.Body)
	}
}

func TestGetMeRejects(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
	}{
		{"missing header", ""},
		{"invalid token", "Bearer bad"},
		{"basic scheme", "Basic dXNlcjpwYXNz"},
		{"no token", "Bearer"},
		{"blank token", "Bearer    "},
		{"token with a space", "Bearer good extra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := &userCalls{}
			a := &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01"}}}
			h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flagsOn(true)))

			rec := request(t, h, "/v1/me", tt.authorization)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if body := decodeError(t, rec); body.Code != "unauthorized" || body.Message != "a valid bearer token is required" {
				t.Errorf("body = %+v", body)
			}
			if len(calls.inputs) != 0 {
				t.Errorf("EnsureUser called for a rejected request: %+v", calls.inputs)
			}
		})
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	calls := &userCalls{user: store.User{ID: uuid.New()}}
	a := &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01"}}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flagsOn(true)))

	if rec := request(t, h, "/v1/me", "bearer  good"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestGetMeUnavailable(t *testing.T) {
	tests := []struct {
		name          string
		auth          *fakeAuth
		authorization string
		wantCode      string
	}{
		{"not configured without a token", &fakeAuth{unconfigured: true}, "", "auth_not_configured"},
		{"not configured with a token", &fakeAuth{unconfigured: true}, "Bearer good", "auth_not_configured"},
		{"keys unavailable", &fakeAuth{err: fmt.Errorf("%w: down", auth.ErrUnavailable)}, "Bearer good", "auth_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(fakeStore{users: &userCalls{}}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(tt.auth), WithFlags(flagsOn(true)))
			rec := request(t, h, "/v1/me", tt.authorization)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
			if body := decodeError(t, rec); body.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Code, tt.wantCode)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if tt.auth.unconfigured && len(tt.auth.seen) != 0 {
				t.Errorf("Verify called while unconfigured: %v", tt.auth.seen)
			}
		})
	}
}

func TestGetMeDefaultsToUnconfigured(t *testing.T) {
	h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test", WithFlags(flagsOn(true)))
	rec := request(t, h, "/v1/me", "Bearer anything")
	if rec.Code != http.StatusServiceUnavailable || decodeError(t, rec).Code != "auth_not_configured" {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestGetMeStoreError(t *testing.T) {
	calls := &userCalls{err: errors.New("connection refused")}
	a := &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01"}}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flagsOn(true)))

	rec := request(t, h, "/v1/me", "Bearer good")
	if rec.Code != http.StatusInternalServerError || decodeError(t, rec).Code != "internal_error" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestGetMeChecksTheTokenBeforeTheFlag(t *testing.T) {
	tests := []struct {
		name          string
		flagOn        bool
		auth          *fakeAuth
		authorization string
		wantStatus    int
		wantCode      string
	}{
		{"flag off, valid token", false, &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01"}}}, "Bearer good", http.StatusNotFound, "not_found"},
		{"flag off, no token", false, &fakeAuth{}, "", http.StatusUnauthorized, "unauthorized"},
		{"flag off, invalid token", false, &fakeAuth{}, "Bearer expired", http.StatusUnauthorized, "unauthorized"},
		{"flag off, keys unavailable", false, &fakeAuth{err: auth.ErrUnavailable}, "Bearer good", http.StatusServiceUnavailable, "auth_unavailable"},
		{"flag off, not configured", false, &fakeAuth{unconfigured: true}, "", http.StatusServiceUnavailable, "auth_not_configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := &userCalls{}
			h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(tt.auth), WithFlags(flagsOn(tt.flagOn)))
			rec := request(t, h, "/v1/me", tt.authorization)
			if rec.Code != tt.wantStatus || decodeError(t, rec).Code != tt.wantCode {
				t.Errorf("status %d, body %s, want %d %s", rec.Code, rec.Body, tt.wantStatus, tt.wantCode)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if len(calls.inputs) != 0 {
				t.Errorf("EnsureUser called: %+v", calls.inputs)
			}
		})
	}
}

func TestFlagIsEvaluatedOnlyForAVerifiedUser(t *testing.T) {
	var targeting []string
	enabled := func(ctx context.Context, _ string) bool {
		targeting = append(targeting, openfeature.TransactionContext(ctx).TargetingKey())
		return ctx.Value(authContextKey{}).(authResult).identity.UserID == "user_01"
	}
	calls := &userCalls{user: store.User{ID: uuid.New()}}
	a := &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01"}, "other": {UserID: "user_02"}}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(enabled))

	for authorization, want := range map[string]int{
		"Bearer good":  http.StatusOK,
		"Bearer other": http.StatusNotFound,
		"Bearer bad":   http.StatusUnauthorized,
		"":             http.StatusUnauthorized,
	} {
		if rec := request(t, h, "/v1/me", authorization); rec.Code != want {
			t.Errorf("%q: status %d, want %d", authorization, rec.Code, want)
		}
	}
	slices.Sort(targeting)
	if want := []string{"user_01", "user_02"}; !slices.Equal(targeting, want) {
		t.Errorf("targeting keys = %q, want %q", targeting, want)
	}
}

func TestPublicEndpointsIgnoreBadTokens(t *testing.T) {
	for name, a := range map[string]*fakeAuth{
		"invalid":        {},
		"unavailable":    {err: auth.ErrUnavailable},
		"not configured": {unconfigured: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewHandler(liveStore(), slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flagsOn(true)))
			for _, path := range []string{"/v1/healthz", "/v1/agencies", "/v1/agencies/la-metro", "/v1/agencies/unknown"} {
				baseline := request(t, h, path, "")
				for _, authorization := range []string{"Bearer expired.token.here", "Basic abc", "Bearer"} {
					rec := request(t, h, path, authorization)
					if rec.Code != baseline.Code || rec.Body.String() != baseline.Body.String() || !maps.EqualFunc(rec.Header(), baseline.Header(), slices.Equal) {
						t.Errorf("%s with %q: %d %v %s, want %d %v %s", path, authorization,
							rec.Code, rec.Header(), rec.Body, baseline.Code, baseline.Header(), baseline.Body)
					}
				}
			}
		})
	}
}

func TestIdentityFromWithoutMiddleware(t *testing.T) {
	if _, err := IdentityFrom(t.Context()); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("err = %v, want ErrNoCredentials", err)
	}
	h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test")
	if _, err := h.requireUser(t.Context()); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("requireUser err = %v, want ErrNoCredentials", err)
	}
}
