package featureflags

import (
	"context"

	"github.com/open-feature/go-sdk/openfeature"
)

const allFlags = "*"

type staticProvider struct {
	openfeature.NoopProvider
	on map[string]bool
}

func newStaticProvider(flags []string) staticProvider {
	on := make(map[string]bool, len(flags))
	for _, flag := range flags {
		on[flag] = true
	}
	return staticProvider{on: on}
}

func (p staticProvider) Metadata() openfeature.Metadata {
	return openfeature.Metadata{Name: "static"}
}

func (p staticProvider) BooleanEvaluation(_ context.Context, flag string, defaultValue bool, _ openfeature.FlattenedContext) openfeature.BoolResolutionDetail {
	if !p.on[flag] && !p.on[allFlags] {
		return openfeature.BoolResolutionDetail{
			Value: defaultValue,
			ProviderResolutionDetail: openfeature.ProviderResolutionDetail{
				ResolutionError: openfeature.NewFlagNotFoundResolutionError(flag),
				Reason:          openfeature.ErrorReason,
			},
		}
	}
	return openfeature.BoolResolutionDetail{
		Value:                    true,
		ProviderResolutionDetail: openfeature.ProviderResolutionDetail{Reason: openfeature.StaticReason},
	}
}
