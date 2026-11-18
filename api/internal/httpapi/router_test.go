package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
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
