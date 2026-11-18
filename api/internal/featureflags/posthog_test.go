package featureflags

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const testPostHogKey = "phc_test"

type flagDetail struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

func fakePostHog(t *testing.T, flags map[string]bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/flags/" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			APIKey     string `json:"api_key"`
			DistinctID string `json:"distinct_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.APIKey != testPostHogKey || req.DistinctID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		details := make(map[string]flagDetail, len(flags))
		for key, enabled := range flags {
			details[key] = flagDetail{Key: key, Enabled: enabled}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"flags": details})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPostHogDecidesWhenKeySet(t *testing.T) {
	srv := fakePostHog(t, map[string]bool{"new-search": true, "digest-emails": false})
	initFlags(t, config.FeatureFlags{PostHogKey: testPostHogKey, PostHogHost: srv.URL, On: []string{"digest-emails"}})

	if !Enabled(t.Context(), "new-search") {
		t.Error("new-search is on in PostHog but evaluated to off")
	}
	if Enabled(t.Context(), "digest-emails") {
		t.Error("digest-emails is off in PostHog but evaluated to on")
	}
}

func TestPostHogUnknownFlagIsOff(t *testing.T) {
	srv := fakePostHog(t, map[string]bool{"new-search": true})
	initFlags(t, config.FeatureFlags{PostHogKey: testPostHogKey, PostHogHost: srv.URL})

	if Enabled(t.Context(), "never-defined") {
		t.Error("a flag PostHog does not know evaluated to on")
	}
}

func TestPostHogErrorIsOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	initFlags(t, config.FeatureFlags{PostHogKey: testPostHogKey, PostHogHost: srv.URL})

	if Enabled(t.Context(), "new-search") {
		t.Error("a flag evaluated to on while PostHog returned an error")
	}
}

func TestInitRejectsInvalidPostHogHost(t *testing.T) {
	cfg := config.FeatureFlags{PostHogKey: testPostHogKey, PostHogHost: "://not-a-url"}
	if _, err := Init(t.Context(), cfg, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("expected an error for an invalid PostHog host")
	}
}

func TestPostHogUnreachableIsOff(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	initFlags(t, config.FeatureFlags{PostHogKey: testPostHogKey, PostHogHost: srv.URL})

	if Enabled(t.Context(), "new-search") {
		t.Error("a flag evaluated to on while PostHog was unreachable")
	}
}
