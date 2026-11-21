package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const shutdownTimeout = 25 * time.Second

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := config.NewLogger(cfg)
	if cfg.Stripe.Problem != "" {
		logger.ErrorContext(ctx, "billing is not configured: the Stripe settings are invalid", "problem", cfg.Stripe.Problem)
	}
	if cfg.Webhooks.Problem != "" {
		logger.ErrorContext(ctx, "webhook subscriptions are not configured: the secret key is invalid", "problem", cfg.Webhooks.Problem)
	}
	if cfg.Stripe.WebhookProblem != "" {
		logger.ErrorContext(ctx, "stripe webhooks are not configured: the signing secret is invalid", "problem", cfg.Stripe.WebhookProblem)
	}

	shutdownFlags, err := featureflags.Init(ctx, cfg.FeatureFlags, logger)
	if err != nil {
		return err
	}
	defer shutdownFlags()

	verifier, err := auth.New(cfg.WorkOS, auth.WithLogger(logger))
	if err != nil {
		return err
	}
	if !verifier.Configured() {
		logger.WarnContext(ctx, "WorkOS is not configured, signed-in endpoints will refuse requests")
	}

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	db := store.New(pool)
	biller := billing.New(cfg.Stripe, cfg.Web.PublicURL, db, billing.WithLogger(logger))
	if biller.Configured() {
		logger.InfoContext(ctx, "billing configured", "stripe_mode", cfg.Stripe.Mode(), "stripe_price", cfg.Stripe.PriceID)
	}
	stripeEvents := billing.NewWebhook(cfg.Stripe, db, billing.WithLogger(logger))
	if stripeEvents.Configured() {
		logger.InfoContext(ctx, "stripe webhooks configured")
	}

	var sealer *webhooks.Sealer
	if cfg.Webhooks.Configured() {
		if sealer, err = webhooks.NewSealer(cfg.Webhooks.SecretKey); err != nil {
			return err
		}
		logger.InfoContext(ctx, "webhook secrets configured")
	}

	inserter, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		return fmt.Errorf("job client: %w", err)
	}

	srv := &http.Server{
		Addr: net.JoinHostPort("", cfg.Port),
		Handler: httpapi.NewRouter(httpapi.NewHandler(db, logger, version,
			httpapi.WithAuthenticator(verifier),
			httpapi.WithPublicWebURL(cfg.Web.PublicURL),
			httpapi.WithBilling(biller),
			httpapi.WithStripeEvents(stripeEvents),
			httpapi.WithWebhookSealer(sealer),
			httpapi.WithWebhookSender(webhooks.NewSender(cfg.Fetch.UserAgent)),
			httpapi.WithSubmissionFollowUp(jobs.EnqueueScreenAgency{Client: inserter, Enabled: featureflags.Enabled}),
		), logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	logger.InfoContext(ctx, "api listening", "addr", srv.Addr, "version", version)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	logger.InfoContext(ctx, "api shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
