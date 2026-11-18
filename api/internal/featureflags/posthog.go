package featureflags

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/posthog/posthog-go"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const serverDistinctID = "civic-scanner-server"

type posthogProvider struct {
	openfeature.NoopProvider
	client posthog.Client
	cache  *flagCache
	logger *slog.Logger
}

func newPostHogProvider(cfg config.FeatureFlags, logger *slog.Logger) (*posthogProvider, error) {
	if err := validateHost(cfg.PostHogHost); err != nil {
		return nil, err
	}
	client, err := posthog.NewWithConfig(cfg.PostHogKey, posthog.Config{
		Endpoint:                     cfg.PostHogHost,
		FeatureFlagRequestTimeout:    cfg.PostHogTimeout,
		FeatureFlagRequestMaxRetries: posthog.Ptr(0),
		Logger:                       posthog.StdLogger(slog.NewLogLogger(logger.Handler(), slog.LevelWarn), false),
	})
	if err != nil {
		return nil, err
	}
	p := &posthogProvider{client: client, logger: logger}
	p.cache = newFlagCache(cfg.PostHogCacheTTL, p.fetchAll, logger)
	return p, nil
}

func (p *posthogProvider) fetchAll(distinctID string) (flagValues, error) {
	return p.client.GetAllFlags(posthog.FeatureFlagPayloadNoKey{
		DistinctId:            distinctID,
		SendFeatureFlagEvents: posthog.Ptr(false),
	})
}

func validateHost(host string) error {
	if host == "" {
		return nil
	}
	u, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("POSTHOG_HOST: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("POSTHOG_HOST %q: want an http or https URL with a host", host)
	}
	return nil
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
	values, err := p.cache.values(ctx, distinctID)
	if err != nil {
		if !errors.Is(err, errRefreshFailed) {
			p.logger.WarnContext(ctx, "feature flag evaluation failed", "flag", flag, "error", err)
		}
		return unresolved(defaultValue, openfeature.NewGeneralResolutionError(err.Error()))
	}
	switch value := values[flag].(type) {
	case nil:
		return unresolved(defaultValue, openfeature.NewFlagNotFoundResolutionError(flag))
	case bool:
		return openfeature.BoolResolutionDetail{
			Value:                    value,
			ProviderResolutionDetail: openfeature.ProviderResolutionDetail{Reason: openfeature.TargetingMatchReason},
		}
	case string:
		p.logger.WarnContext(ctx, "multivariate feature flag evaluated as boolean", "flag", flag, "variant", value)
		return unresolved(defaultValue, openfeature.NewTypeMismatchResolutionError(flag+" is multivariate"))
	default:
		return unresolved(defaultValue, openfeature.NewTypeMismatchResolutionError(flag+" is not boolean"))
	}
}

func unresolved(defaultValue bool, err openfeature.ResolutionError) openfeature.BoolResolutionDetail {
	return openfeature.BoolResolutionDetail{
		Value: defaultValue,
		ProviderResolutionDetail: openfeature.ProviderResolutionDetail{
			ResolutionError: err,
			Reason:          openfeature.ErrorReason,
		},
	}
}
