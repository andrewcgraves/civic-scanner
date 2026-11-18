package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const shutdownTimeout = 25 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
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

	firecrawl := fetch.NewFirecrawl(cfg.Fetch)
	fetcher := fetch.NewFallback(fetch.NewPlain(cfg.Fetch), firecrawl)
	pdfExtractor := extract.NewSubprocess(cfg.Extract)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fetcher, Clock: sources.SystemClock{}, Logger: logger}, adapters)
	client, err := jobs.NewClient(pool, logger, jobs.Config{
		Registry:     registry,
		Fetcher:      fetcher,
		Fallback:     firecrawl,
		Extractor:    &extract.Extractor{PDF: pdfExtractor},
		CheckHourUTC: cfg.Ingest.CheckHourUTC,
		LLM:          llm.New(cfg.LLM),
		SummaryTier:  llm.Tier(cfg.Summary.AgendaTier),
		LLMTimeout:   cfg.LLM.Timeout,
	})
	if err != nil {
		return err
	}
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	logger.InfoContext(ctx, "worker started", "source_adapters", registry.Slugs(), "check_hour_utc", cfg.Ingest.CheckHourUTC,
		"pdf_extractor", pdfExtractor.Binary, "agenda_summary_tier", cfg.Summary.AgendaTier, "llm_configured", cfg.LLM.APIKey != "")
	if _, err := os.Stat(pdfExtractor.Binary); err != nil {
		logger.WarnContext(ctx, "pdf extractor not found: PDF agendas will be cancelled; build it next to the worker or set EXTRACT_PDF_BINARY",
			"pdf_extractor", pdfExtractor.Binary, "error", err)
	}

	<-ctx.Done()
	logger.InfoContext(ctx, "worker shutting down")
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	return client.Stop(stopCtx)
}
