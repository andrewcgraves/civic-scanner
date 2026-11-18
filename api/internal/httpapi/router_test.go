package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
)

func serveWithRecoverer(h http.HandlerFunc) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	recoverer(slog.New(slog.DiscardHandler))(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

func TestRecovererReturnsInternalError(t *testing.T) {
	rec := serveWithRecoverer(func(http.ResponseWriter, *http.Request) { panic("boom") })

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if want := `{"code":"internal_error","message":"internal server error"}` + "\n"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestRecovererPassesThroughWithoutPanic(t *testing.T) {
	rec := serveWithRecoverer(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestRecovererRepanicsOnAbortHandler(t *testing.T) {
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler { //nolint:errorlint
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	serveWithRecoverer(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	t.Error("expected http.ErrAbortHandler to propagate")
}

func serveRequest(method, path string) *httptest.ResponseRecorder {
	logger := slog.New(slog.DiscardHandler)
	rec := httptest.NewRecorder()
	NewRouter(NewHandler(fakeStore{}, logger, "test"), logger).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestRouterRejectsWrongMethodWithErrorJSON(t *testing.T) {
	for _, path := range []string{"/v1/agencies", "/v1/healthz"} {
		rec := serveRequest(http.MethodPost, path)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want 405", path, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != "GET" {
			t.Errorf("POST %s Allow = %q, want GET", path, allow)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("POST %s Content-Type = %q, want application/json", path, ct)
		}
		var body v1.Error
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "method_not_allowed" || body.Message == "" {
			t.Errorf("POST %s body = %q (%v), want method_not_allowed Error", path, rec.Body.String(), err)
		}
	}
}

func TestRouterRejectsUnknownPathWithErrorJSON(t *testing.T) {
	rec := serveRequest(http.MethodGet, "/v1/nope")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body v1.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "not_found" || body.Message == "" {
		t.Errorf("body = %q (%v), want not_found Error", rec.Body.String(), err)
	}
}
