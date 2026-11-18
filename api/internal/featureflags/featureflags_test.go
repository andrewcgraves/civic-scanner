package featureflags

import (
	"log/slog"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

func initFlags(t *testing.T, cfg config.FeatureFlags) {
	t.Helper()
	shutdown, err := Init(t.Context(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(shutdown)
}

func TestUnknownFlagIsOff(t *testing.T) {
	initFlags(t, config.FeatureFlags{})

	if Enabled(t.Context(), "never-defined") {
		t.Error("an unknown flag evaluated to on")
	}
}

func TestListedFlagsAreOn(t *testing.T) {
	initFlags(t, config.FeatureFlags{On: []string{"new-search", "digest-emails"}})

	for _, flag := range []string{"new-search", "digest-emails"} {
		if !Enabled(t.Context(), flag) {
			t.Errorf("%s evaluated to off", flag)
		}
	}
	if Enabled(t.Context(), "beta-map") {
		t.Error("an unlisted flag evaluated to on")
	}
}

func TestWildcardTurnsEveryFlagOn(t *testing.T) {
	initFlags(t, config.FeatureFlags{On: []string{"*"}})

	for _, flag := range []string{"new-search", "anything-at-all"} {
		if !Enabled(t.Context(), flag) {
			t.Errorf("%s evaluated to off with the wildcard set", flag)
		}
	}
}
