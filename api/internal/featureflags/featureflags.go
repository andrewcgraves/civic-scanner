package featureflags

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/open-feature/go-sdk/openfeature"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

func Init(ctx context.Context, cfg config.FeatureFlags, logger *slog.Logger) (func(), error) {
	provider, err := newProvider(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("feature flags: %w", err)
	}
	if err := openfeature.SetProviderWithContextAndWait(ctx, provider); err != nil {
		return nil, fmt.Errorf("feature flags: %w", err)
	}
	logger.InfoContext(ctx, "feature flags ready", "provider", provider.Metadata().Name)
	return openfeature.Shutdown, nil
}

func Enabled(ctx context.Context, flag string) bool {
	return openfeature.NewDefaultClient().Boolean(ctx, flag, false, openfeature.EvaluationContext{})
}

func WithIdentity(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return openfeature.MergeTransactionContext(ctx, openfeature.NewEvaluationContext(id, nil))
}

func newProvider(cfg config.FeatureFlags, logger *slog.Logger) (openfeature.FeatureProvider, error) {
	if cfg.PostHogKey != "" {
		return newPostHogProvider(cfg, logger)
	}
	return newStaticProvider(cfg.On), nil
}
