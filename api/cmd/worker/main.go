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
	"github.com/andrewcgraves/civic-scanner/api/internal/media"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
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

	bucket, err := storage.New(cfg.Storage)
	if err != nil {
		return err
	}

	plain := fetch.NewPlain(cfg.Fetch)
	firecrawl := fetch.NewFirecrawl(cfg.Fetch)
	fetcher := fetch.NewFallback(plain, firecrawl)
	audio := media.NewDownloader(cfg.Audio, plain, cfg.Fetch.UserAgent)
	pdfExtractor := extract.NewSubprocess(cfg.Extract)
	registry := sources.NewRegistry(sources.Deps{Fetcher: fetcher, Clock: sources.SystemClock{}, Logger: logger}, adapters)
	client, err := jobs.NewClient(pool, logger, jobs.Config{
		Registry:     registry,
		Fetcher:      fetcher,
		Fallback:     firecrawl,
		Extractor:    &extract.Extractor{PDF: pdfExtractor},
		CheckHourUTC: cfg.Ingest.CheckHourUTC,
		Storage:      bucket,
		Audio:        audio,
		AudioTimeout: cfg.Audio.Timeout,
	})
	if err != nil {
		return err
	}
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	logger.InfoContext(ctx, "worker started", "source_adapters", registry.Slugs(), "check_hour_utc", cfg.Ingest.CheckHourUTC,
		"pdf_extractor", pdfExtractor.Binary, "audio_limit", audio.Tool.Limit, "ffmpeg", audio.Tool.FFmpeg, "yt_dlp", audio.Tool.YTDLP,
		"storage_configured", cfg.Storage.Configured())
	if _, err := os.Stat(pdfExtractor.Binary); err != nil {
		logger.WarnContext(ctx, "pdf extractor not found: PDF agendas will be cancelled; build it next to the worker or set EXTRACT_PDF_BINARY",
			"pdf_extractor", pdfExtractor.Binary, "error", err)
	}
	if _, err := os.Stat(audio.Tool.Limit); err != nil {
		logger.WarnContext(ctx, "audio limit binary not found: recording downloads will be cancelled; build it next to the worker or set AUDIO_LIMIT_BINARY",
			"audio_limit", audio.Tool.Limit, "error", err)
	}

	<-ctx.Done()
	logger.InfoContext(ctx, "worker shutting down")
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	return client.Stop(stopCtx)
}
