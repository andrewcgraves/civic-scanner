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

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
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

	shutdownFlags, err := featureflags.Init(ctx, cfg.FeatureFlags, logger)
	if err != nil {
		return err
	}
	defer shutdownFlags()

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	srv := &http.Server{
		Addr:              net.JoinHostPort("", cfg.Port),
		Handler:           httpapi.NewRouter(httpapi.NewHandler(store.New(pool), logger, version), logger),
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
