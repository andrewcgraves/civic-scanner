//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/migrations"
)

func TestAgenciesAgainstPostgres(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Fatal("DATABASE_URL must be set for integration tests")
	}
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)

	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Up(ctx, pool, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const slug = "integration-test-agency"
	if _, err := pool.Exec(ctx,
		`INSERT INTO agencies (slug, name) VALUES ($1, $2) ON CONFLICT (slug) DO NOTHING`,
		slug, "Integration Test Agency"); err != nil {
		t.Fatalf("insert agency: %v", err)
	}
	t.Cleanup(func() {
		// t.Context() is already cancelled when cleanups run.
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE slug = $1`, slug)
	})

	srv := httptest.NewServer(httpapi.NewRouter(httpapi.NewHandler(store.New(pool), logger, "test"), logger))
	t.Cleanup(srv.Close)

	var health v1.Health
	getJSON(t, srv.URL+"/v1/healthz", http.StatusOK, &health)
	if health.Status != v1.Ok {
		t.Errorf("health status = %q, want ok", health.Status)
	}

	var list v1.AgencyList
	getJSON(t, srv.URL+"/v1/agencies", http.StatusOK, &list)
	for _, a := range list.Agencies {
		if a.Slug == slug {
			if a.Name != "Integration Test Agency" || a.Id.String() == "00000000-0000-0000-0000-000000000000" {
				t.Errorf("unexpected agency: %+v", a)
			}
			return
		}
	}
	t.Errorf("agency %q not in response: %+v", slug, list.Agencies)
}

func getJSON(t *testing.T, url string, wantStatus int, out any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test request to a local httptest server
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s: status %d, want %d", url, resp.StatusCode, wantStatus)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: decode: %v", url, err)
	}
}
