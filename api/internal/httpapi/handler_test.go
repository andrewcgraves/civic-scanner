package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

type fakeStore struct {
	pingErr  error
	agencies []store.Agency
	listErr  error
}

func (f fakeStore) Ping(context.Context) error { return f.pingErr }

func (f fakeStore) ListAgencies(context.Context) ([]store.Agency, error) {
	return f.agencies, f.listErr
}

func get(t *testing.T, s Store, path string, out any) int {
	t.Helper()
	router := NewRouter(NewHandler(s, slog.New(slog.DiscardHandler), "test"), slog.New(slog.DiscardHandler))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantCode   int
		wantStatus v1.HealthStatus
	}{
		{"ok", nil, http.StatusOK, v1.Ok},
		{"database down", errors.New("connection refused"), http.StatusServiceUnavailable, v1.Degraded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body v1.Health
			code := get(t, fakeStore{pingErr: tt.pingErr}, "/v1/healthz", &body)
			if code != tt.wantCode || body.Status != tt.wantStatus {
				t.Errorf("got %d %q, want %d %q", code, body.Status, tt.wantCode, tt.wantStatus)
			}
			if body.Version == nil || *body.Version != "test" {
				t.Errorf("version = %v, want test", body.Version)
			}
		})
	}
}

func TestListAgencies(t *testing.T) {
	id := uuid.New()
	var body v1.AgencyList
	code := get(t, fakeStore{agencies: []store.Agency{{ID: id, Slug: "la-metro", Name: "LA Metro"}}}, "/v1/agencies", &body)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	want := v1.Agency{Id: id, Slug: "la-metro", Name: "LA Metro"}
	if len(body.Agencies) != 1 || body.Agencies[0] != want {
		t.Errorf("agencies = %+v, want [%+v]", body.Agencies, want)
	}
}

func TestListAgenciesEmptyIsArray(t *testing.T) {
	var body map[string]json.RawMessage
	get(t, fakeStore{}, "/v1/agencies", &body)
	if string(body["agencies"]) != "[]" {
		t.Errorf("agencies = %s, want []", body["agencies"])
	}
}

func TestListAgenciesError(t *testing.T) {
	var body v1.Error
	code := get(t, fakeStore{listErr: errors.New("boom")}, "/v1/agencies", &body)
	if code != http.StatusInternalServerError || body.Code != "internal_error" {
		t.Errorf("got %d %+v", code, body)
	}
}

func TestUnversionedPathNotFound(t *testing.T) {
	if code := get(t, fakeStore{}, "/healthz", nil); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}
