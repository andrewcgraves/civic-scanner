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
	if rec.Code != http.StatusInternalServerError || decodeError(t, rec).Code != "internal_error" {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestGetMeIsNotFoundWhileTheFlagIsOff(t *testing.T) {
	calls := &userCalls{}
	a := &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01"}}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flagsOn(false)))

	for _, authorization := range []string{"", "Bearer good", "Bearer bad"} {
		rec := request(t, h, "/v1/me", authorization)
		if rec.Code != http.StatusNotFound || decodeError(t, rec).Code != "not_found" {
			t.Errorf("%q: status %d, body %s", authorization, rec.Code, rec.Body)
		}
	}
	if len(calls.inputs) != 0 {
		t.Errorf("EnsureUser called with the flag off: %+v", calls.inputs)
	}
}

func TestFlagIsEvaluatedForTheSignedInUser(t *testing.T) {
	var targeting []string
	enabled := func(ctx context.Context, _ string) bool {
		targeting = append(targeting, openfeature.TransactionContext(ctx).TargetingKey())
		return false
	}
	a := &fakeAuth{tokens: map[string]auth.Identity{"good": {UserID: "user_01"}}}
	h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(enabled))

	request(t, h, "/v1/me", "Bearer good")
	request(t, h, "/v1/me", "Bearer bad")
	if len(targeting) != 2 || targeting[0] != "user_01" || targeting[1] != "" {
		t.Errorf("targeting keys = %q, want [user_01 \"\"]", targeting)
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
			for _, authorization := range []string{"Bearer expired.token.here", "Basic abc", "Bearer"} {
				for _, path := range []string{"/v1/healthz", "/v1/agencies", "/v1/agencies/la-metro"} {
					if rec := request(t, h, path, authorization); rec.Code != http.StatusOK {
						t.Errorf("%s with %q: status %d", path, authorization, rec.Code)
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
}
