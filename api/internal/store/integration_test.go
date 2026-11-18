//go:build integration

package store_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestListAgenciesOrdersByName(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	seeded := map[string]string{"store-test-zeta": "Store Test Zeta", "store-test-alpha": "Store Test Alpha"}
	for slug, name := range seeded {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $2, 'live', 'https://example.com') ON CONFLICT (slug) DO NOTHING`, slug, name); err != nil {
			t.Fatalf("insert agency: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE slug LIKE 'store-test-%'`)
	})

	agencies, err := s.ListAgencies(ctx)
	if err != nil {
		t.Fatalf("ListAgencies: %v", err)
	}

	var got []string
	for _, a := range agencies {
		if _, ok := seeded[a.Slug]; !ok {
			continue
		}
		if a.Name != seeded[a.Slug] || a.CreatedAt.IsZero() {
			t.Errorf("unexpected agency: %+v", a)
		}
		got = append(got, a.Slug)
	}
	if want := []string{"store-test-alpha", "store-test-zeta"}; !slices.Equal(got, want) {
		t.Errorf("seeded slugs in order = %v, want %v", got, want)
	}
}

func TestListAgenciesReturnsOnlyLiveAgenciesWithWebsite(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)

	for _, row := range []struct{ slug, status string }{
		{"store-test-live", "live"},
		{"store-test-pending", "pending"},
		{"store-test-rejected", "rejected"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, $2, $3) ON CONFLICT (slug) DO NOTHING`,
			row.slug, row.status, "https://"+row.slug+".example.com"); err != nil {
			t.Fatalf("insert agency: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE slug LIKE 'store-test-%'`)
	})

	agencies, err := store.New(pool).ListAgencies(ctx)
	if err != nil {
		t.Fatalf("ListAgencies: %v", err)
	}

	got := map[string]string{}
	for _, a := range agencies {
		if strings.HasPrefix(a.Slug, "store-test-") {
			got[a.Slug] = a.Website
		}
	}
	want := map[string]string{"store-test-live": "https://store-test-live.example.com"}
	if !maps.Equal(got, want) {
		t.Errorf("store-test agencies = %v, want %v", got, want)
	}
}

func TestListAgenciesFailsOnClosedPool(t *testing.T) {
	pool, err := store.Open(t.Context(), testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()

	_, err = store.New(pool).ListAgencies(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "query agencies:") {
		t.Fatalf("err = %v, want query agencies error", err)
	}
}
