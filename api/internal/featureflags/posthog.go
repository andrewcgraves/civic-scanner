package featureflags

import (
	"context"
	"errors"
	"log/slog"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/posthog/posthog-go"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const serverDistinctID = "civic-scanner-server"

type posthogProvider struct {
	openfeature.NoopProvider
	client posthog.Client
	logger *slog.Logger
}

func newPostHogProvider(cfg config.FeatureFlags, logger *slog.Logger) (*posthogProvider, error) {
	client, err := posthog.NewWithConfig(cfg.PostHogKey, posthog.Config{
		Endpoint: cfg.PostHogHost,
		Logger:   posthog.StdLogger(slog.NewLogLogger(logger.Handler(), slog.LevelWarn), false),
	})
	if err != nil {
		return nil, err
	}
	return &posthogProvider{client: client, logger: logger}, nil
}

func (p *posthogProvider) Metadata() openfeature.Metadata {
	return openfeature.Metadata{Name: "posthog"}
}

func (p *posthogProvider) Init(openfeature.EvaluationContext) error {
	return nil
}

func (p *posthogProvider) Shutdown() {
	if err := p.client.Close(); err != nil {
		p.logger.Warn("posthog client close failed", "error", err)
	}
}

func (p *posthogProvider) BooleanEvaluation(ctx context.Context, flag string, defaultValue bool, flatCtx openfeature.FlattenedContext) openfeature.BoolResolutionDetail {
	distinctID, _ := flatCtx[openfeature.TargetingKey].(string)
	if distinctID == "" {
		distinctID = serverDistinctID
	}
	result, err := p.client.GetFeatureFlagResult(posthog.FeatureFlagPayload{
		Key:                   flag,
		DistinctId:            distinctID,
		SendFeatureFlagEvents: posthog.Ptr(false),
	})
	if err != nil {
		resolutionErr := openfeature.NewGeneralResolutionError(err.Error())
		if errors.Is(err, posthog.ErrFlagNotFound) {
			resolutionErr = openfeature.NewFlagNotFoundResolutionError(flag)
		} else {
			p.logger.WarnContext(ctx, "feature flag evaluation failed", "flag", flag, "error", err)
		}
		return openfeature.BoolResolutionDetail{
			Value: defaultValue,
			ProviderResolutionDetail: openfeature.ProviderResolutionDetail{
				ResolutionError: resolutionErr,
				Reason:          openfeature.ErrorReason,
			},
		}
	}
	return openfeature.BoolResolutionDetail{
		Value:                    result.Enabled,
		ProviderResolutionDetail: openfeature.ProviderResolutionDetail{Reason: openfeature.TargetingMatchReason},
	}
}
