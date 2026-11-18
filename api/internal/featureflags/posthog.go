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
	logger *slog.Logger
}

type flagResult struct {
	result *posthog.FeatureFlagResult
	err    error
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
	return &posthogProvider{client: client, logger: logger}, nil
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
	result, err := p.evaluate(ctx, flag, distinctID)
	switch {
	case errors.Is(err, posthog.ErrFlagNotFound):
		return unresolved(defaultValue, openfeature.NewFlagNotFoundResolutionError(flag))
	case err != nil:
		p.logger.WarnContext(ctx, "feature flag evaluation failed", "flag", flag, "error", err)
		return unresolved(defaultValue, openfeature.NewGeneralResolutionError(err.Error()))
	case result.Variant != nil:
		p.logger.WarnContext(ctx, "multivariate feature flag evaluated as boolean", "flag", flag, "variant", *result.Variant)
		return unresolved(defaultValue, openfeature.NewTypeMismatchResolutionError(flag+" is multivariate"))
	}
	return openfeature.BoolResolutionDetail{
		Value:                    result.Enabled,
		ProviderResolutionDetail: openfeature.ProviderResolutionDetail{Reason: openfeature.TargetingMatchReason},
	}
}

func (p *posthogProvider) evaluate(ctx context.Context, flag, distinctID string) (*posthog.FeatureFlagResult, error) {
	done := make(chan flagResult, 1)
	go func() {
		result, err := p.client.GetFeatureFlagResult(posthog.FeatureFlagPayload{
			Key:                   flag,
			DistinctId:            distinctID,
			SendFeatureFlagEvents: posthog.Ptr(false),
		})
		done <- flagResult{result: result, err: err}
	}()
	select {
	case r := <-done:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
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
