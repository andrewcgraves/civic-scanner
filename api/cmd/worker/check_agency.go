package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	checkAgencyCommand = "check-agency"
	allAgenciesFlag    = "--all"
)

var (
	errCheckUsage   = errors.New("usage: worker " + checkAgencyCommand + " (<agency-slug> | " + allAgenciesFlag + ")")
	errNotMigrated  = errors.New("database is not migrated: run /app/migrate up first; nothing enqueued")
	errNothingToDo  = errors.New("no live agency has a source adapter; nothing enqueued")
	errCheckFlagOff = fmt.Errorf("feature flag %s is off, so the worker would skip the check; turn it on first; nothing enqueued", jobs.FlagCheckAgencies)
	errWorkerUsage  = errors.New("usage: worker [" + checkAgencyCommand + " (<agency-slug> | " + allAgenciesFlag + ")]")
)

type checkTarget struct {
	slug string
	all  bool
}

type checkPlan struct {
	slugs     []string
	noAdapter []string
}

func parseCheckArgs(args []string) (checkTarget, error) {
	if len(args) != 1 || args[0] == "" {
		return checkTarget{}, errCheckUsage
	}
	if args[0] == allAgenciesFlag {
		return checkTarget{all: true}, nil
	}
	if strings.HasPrefix(args[0], "-") {
		return checkTarget{}, errCheckUsage
	}
	return checkTarget{slug: args[0]}, nil
}

func planChecks(target checkTarget, live []store.Agency, adapterSlugs []string) (checkPlan, error) {
	var plan checkPlan
	var checkable []string
	for _, a := range live {
		if slices.Contains(adapterSlugs, a.Slug) {
			checkable = append(checkable, a.Slug)
		} else {
			plan.noAdapter = append(plan.noAdapter, a.Slug)
		}
	}
	slices.Sort(checkable)
	slices.Sort(plan.noAdapter)

	if target.all {
		if len(checkable) == 0 {
			return checkPlan{}, errNothingToDo
		}
		plan.slugs = checkable
		return plan, nil
	}

	switch {
	case slices.Contains(checkable, target.slug):
		return checkPlan{slugs: []string{target.slug}}, nil
	case slices.Contains(plan.noAdapter, target.slug):
		return checkPlan{}, fmt.Errorf("agency %q is live but has no source adapter; nothing enqueued", target.slug)
	default:
		return checkPlan{}, fmt.Errorf("agency %q is unknown or not live; nothing enqueued (live agencies with a source adapter: %s)",
			target.slug, listOrNone(checkable))
	}
}

func listOrNone(slugs []string) string {
	if len(slugs) == 0 {
		return "none"
	}
	return strings.Join(slugs, ", ")
}

func adapterSlugs() []string {
	return slices.Sorted(maps.Keys(adapters))
}

func checkAgency(ctx context.Context, args []string, stdout io.Writer, queue string) error {
	target, err := parseCheckArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: max(cfg.LogLevel, slog.LevelWarn)}))

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := requireSchema(ctx, pool); err != nil {
		return err
	}

	live, err := store.New(pool).ListAgencies(ctx)
	if err != nil {
		return err
	}
	plan, err := planChecks(target, live, adapterSlugs())
	if err != nil {
		return err
	}

	shutdownFlags, err := featureflags.Init(ctx, cfg.FeatureFlags, logger)
	if err != nil {
		return err
	}
	defer shutdownFlags()
	if !featureflags.Enabled(ctx, jobs.FlagCheckAgencies) {
		return errCheckFlagOff
	}

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		return err
	}
	checks, err := jobs.EnqueueManualChecks(ctx, client, queue, plan.slugs)
	if err != nil {
		return err
	}
	return report(stdout, queue, checks, plan.noAdapter, target.all)
}

func requireSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var migrated bool
	err := pool.QueryRow(ctx, `SELECT to_regclass('river_job') IS NOT NULL AND to_regclass('agencies') IS NOT NULL`).Scan(&migrated)
	if err != nil {
		return fmt.Errorf("check database schema: %w", err)
	}
	if !migrated {
		return errNotMigrated
	}
	return nil
}

func report(w io.Writer, queue string, checks []jobs.ManualCheck, noAdapter []string, all bool) error {
	var b strings.Builder
	inserted := 0
	for _, c := range checks {
		if c.Duplicate {
			fmt.Fprintf(&b, "%s: not enqueued again, manual check job %d is still %s\n", c.AgencySlug, c.JobID, c.State)
			continue
		}
		inserted++
		fmt.Fprintf(&b, "%s: enqueued manual check, job %d\n", c.AgencySlug, c.JobID)
	}
	if all {
		for _, slug := range noAdapter {
			fmt.Fprintf(&b, "%s: skipped, no source adapter\n", slug)
		}
	}
	fmt.Fprintf(&b, "%d manual check(s) enqueued on queue %q, %d already waiting or running; the worker logs %q when each one starts\n",
		inserted, queue, len(checks)-inserted, "agency check triggered manually")
	_, err := io.WriteString(w, b.String())
	return err
}
