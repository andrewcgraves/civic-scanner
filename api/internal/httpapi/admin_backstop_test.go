package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var meAsAdmin = map[string]adminRoute{"GetMe": {method: http.MethodGet, pattern: "/v1/me"}}

func headAsGet(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rctx := chi.RouteContext(r.Context()); rctx != nil && r.Method == http.MethodHead {
			rctx.RouteMethod = http.MethodGet
		}
		next.ServeHTTP(w, r)
	})
}

func serve(router http.Handler, method, path, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestBackstopRefusesWhatARewritingMiddlewareSlipsPastTheGate(t *testing.T) {
	for _, tt := range []struct {
		name     string
		role     store.Role
		backstop map[string]adminRoute
		want     int
	}{
		{"without the backstop the rewrite reaches the handler", store.RoleUser, map[string]adminRoute{}, http.StatusOK},
		{"user", store.RoleUser, meAsAdmin, http.StatusForbidden},
		{"admin", store.RoleAdmin, meAsAdmin, http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := &userCalls{user: store.User{ID: uuid.New(), Role: tt.role}}
			h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
			logger := slog.New(slog.DiscardHandler)
			router := newRouter(h, logger, meAsAdmin, func(r chi.Router) {
				r.Use(headAsGet)
				apiRoutes(h, logger, tt.backstop)(r)
			})

			rec := serve(router, http.MethodHead, "/v1/me", "Bearer good")
			if rec.Code != tt.want {
				t.Fatalf("HEAD /v1/me: status = %d, want %d; body %s", rec.Code, tt.want, rec.Body)
			}
			if tt.want == http.StatusForbidden {
				if body := decodeError(t, rec); body != forbidden {
					t.Errorf("body = %+v, want %+v", body, forbidden)
				}
				if got := rec.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", got)
				}
				if len(calls.inputs) != 0 {
					t.Errorf("handler looked the user up %d times", len(calls.inputs))
				}
			}
		})
	}
}

func TestBackstopCoversARouteTheGateDoesNotKnow(t *testing.T) {
	misrouted := map[string]adminRoute{"GetMe": {method: http.MethodGet, pattern: "/v1/mee"}}
	for _, role := range []store.Role{store.RoleUser, store.RoleAdmin} {
		calls := &userCalls{user: store.User{ID: uuid.New(), Role: role}}
		h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
		logger := slog.New(slog.DiscardHandler)
		router := newRouter(h, logger, misrouted, apiRoutes(h, logger, misrouted))

		rec := serve(router, http.MethodGet, "/v1/me", "Bearer good")
		if rec.Code != http.StatusForbidden || decodeError(t, rec) != forbidden || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: status %d, body %s, Cache-Control %q; want 403 forbidden with no-store", role, rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestBackstopLetsTheGatedAdminThrough(t *testing.T) {
	calls := &userCalls{user: store.User{ID: uuid.New(), Role: store.RoleAdmin}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
	logger := slog.New(slog.DiscardHandler)
	router := newRouter(h, logger, meAsAdmin, apiRoutes(h, logger, meAsAdmin))

	if rec := serve(router, http.MethodGet, "/v1/me", "Bearer good"); rec.Code != http.StatusOK || len(calls.inputs) != 2 {
		t.Errorf("status %d, lookups %d; want 200 after the gate's lookup and the handler's own", rec.Code, len(calls.inputs))
	}
}

func TestMethodAndSlashVariantsNeverReachAGatedHandler(t *testing.T) {
	calls := &userCalls{user: store.User{ID: uuid.New(), Role: store.RoleUser}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
	logger := slog.New(slog.DiscardHandler)
	probe := &adminProbe{}
	testRouter := adminTestRouter(h, probe, testAdminRoutes)
	apiRouter := newRouter(h, logger, meAsAdmin, apiRoutes(h, logger, meAsAdmin))
	id := uuid.NewString()

	for _, tt := range []struct {
		router http.Handler
		method string
		path   string
		want   int
	}{
		{testRouter, http.MethodHead, "/v1/test-admin/" + id, http.StatusMethodNotAllowed},
		{testRouter, http.MethodGet, "/v1/test-admin/" + id, http.StatusMethodNotAllowed},
		{testRouter, http.MethodPost, "/v1/test-admin/" + id + "/", http.StatusNotFound},
		{testRouter, http.MethodPost, "/v1/test-admin//" + id, http.StatusNotFound},
		{apiRouter, http.MethodHead, "/v1/me", http.StatusMethodNotAllowed},
		{apiRouter, http.MethodGet, "/v1/me/", http.StatusNotFound},
		{apiRouter, http.MethodGet, "/v1//me", http.StatusNotFound},
	} {
		for _, authorization := range []string{"", "Bearer good"} {
			if rec := serve(tt.router, tt.method, tt.path, authorization); rec.Code != tt.want {
				t.Errorf("%s %s (%q): status = %d, want %d", tt.method, tt.path, authorization, rec.Code, tt.want)
			}
		}
	}
	if probe.calls != 0 || len(calls.inputs) != 0 {
		t.Errorf("handlers ran %d times and looked users up %d times", probe.calls, len(calls.inputs))
	}
}

type countingFinder struct{ calls int }

func (f *countingFinder) Find(*chi.Context, string, string) string {
	f.calls++
	return ""
}

func TestAdminGateSkipsTheLookupWithoutAdminOperations(t *testing.T) {
	h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test")
	finder := &countingFinder{}
	reached := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ })

	serve(h.adminGate(finder, map[string]adminRoute{})(next), http.MethodGet, "/v1/me", "")
	if finder.calls != 0 || reached != 1 {
		t.Errorf("empty set: route lookups %d, handler runs %d; want 0 and 1", finder.calls, reached)
	}
	serve(h.adminGate(finder, meAsAdmin)(next), http.MethodGet, "/v1/agencies", "")
	if finder.calls != 1 || reached != 2 {
		t.Errorf("non-empty set: route lookups %d, handler runs %d; want 1 and 2", finder.calls, reached)
	}
}

func middlewareName(mw func(http.Handler) http.Handler) string {
	name := runtime.FuncForPC(reflect.ValueOf(mw).Pointer()).Name()
	return name[strings.LastIndex(name, "/")+1:]
}

func TestRootMiddlewareOrderKeepsTheGateLast(t *testing.T) {
	h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test")
	mux, ok := NewRouter(h, slog.New(slog.DiscardHandler)).(*chi.Mux)
	if !ok {
		t.Fatal("NewRouter does not return a *chi.Mux")
	}
	var got []string
	for _, mw := range mux.Middlewares() {
		got = append(got, middlewareName(mw))
	}
	want := []string{
		"middleware.RequestID",
		"httpapi.requestLogger.func1",
		"httpapi.recoverer.func1",
		"httpapi.joinHeaderLines.func1",
		"httpapi.authenticate.func1",
		"httpapi.(*Handler).adminGate.func1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("root middleware changed:\n got %v\nwant %v\n"+
			"The admin gate must run right after authenticate and last, because it decides from the request's method and path: "+
			"a middleware after it that rewrites the method or path (middleware.GetHead, StripSlashes, a path rewriter) could route a request to an admin handler the gate never checked. "+
			"Put new middleware before authenticate, then update this list.", got, want)
	}
}
