package main

import (
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

func TestLAMetroIsRegisteredAsALegistarAdapter(t *testing.T) {
	fake := fetchtest.Returning("[]")
	now := time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fake, Clock: sourcestest.NewClock(now)}, adapters)

	adapter, ok := registry.Lookup("la-metro")
	if !ok {
		t.Fatalf("no adapter for la-metro; registered: %v", registry.Slugs())
	}
	if _, err := adapter.Meetings(t.Context(), sources.Window{From: now, To: now.AddDate(0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].URL, "https://webapi.legistar.com/v1/metro/events?") {
		t.Errorf("requests = %+v, want one to the metro Legistar events API", calls)
	}
}
