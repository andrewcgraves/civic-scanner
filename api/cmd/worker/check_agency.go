package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	checkAgencyCommand    = "check-agency"
	allAgenciesFlag       = "--all"
	commandConnectTimeout = 10 * time.Second
)

var (
	errCheckUsage   = errors.New("usage: worker " + checkAgencyCommand + " (<agency-slug> | " + allAgenciesFlag + ")")
	errNotMigrated  = errors.New("database is not migrated: run /app/migrate up first; nothing enqueued")
	errNothingToDo  = errors.New("no live agency has a source adapter; nothing enqueued")
	errCheckFlagOff = fmt.Errorf("feature flag %s is off or could not be evaluated (see any warnings above on stderr), so the worker would skip the check; nothing enqueued", jobs.FlagCheckAgencies)
	errWorkerUsage  = errors.New("usage: worker [" + checkAgencyCommand + " (<agency-slug> | " + allAgenciesFlag + ") | " + setRoleCommand + " (<user-id> | <email>) (user | admin)]")
)

const workerHelp = `usage: worker [check-agency (<agency-slug> | --all) | set-role (<user-id> | <email>) (user | admin)]

With no arguments, runs the worker.

  check-agency <agency-slug>      enqueue a manual check of one live agency
  check-agency --all              enqueue a manual check of every live agency with a source adapter
  set-role <user> (user | admin)  make a user an admin, or take admin away
  -h, --help, help                print this help
`

const checkHelp = `usage: worker check-agency (<agency-slug> | --all)

Enqueues a manual check_agency job, which also re-reads every agenda in the
check window, and exits. It needs DATABASE_URL and the ingest-check-agencies
flag on, and never starts the worker.

  <agency-slug>     check one live agency that has a source adapter
  --all             check every live agency that has a source adapter
  -h, --help, help  print this help
`

type checkTarget struct {
	slug string
	all  bool
	help bool
}

func isHelp(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

type checkPlan struct {
	slugs     []string
	noAdapter []string
}

func parseCheckArgs(args []string) (checkTarget, error) {
	if len(args) != 1 || args[0] == "" {
		return checkTarget{}, errCheckUsage
	}
	if isHelp(args[0]) {
		return checkTarget{help: true}, nil
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

func checkAgency(ctx context.Context, args []string, stdout, stderr io.Writer, queue string) error {
	target, err := parseCheckArgs(args)
	if err != nil {
		return err
	}
	if target.help {
		_, err := io.WriteString(stdout, checkHelp)
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	pool, err := openCommandPool(ctx, cfg.DatabaseURL)
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

func commandPoolConfig(databaseURL string) (*pgxpool.Config, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if t := poolCfg.ConnConfig.ConnectTimeout; t <= 0 || t > commandConnectTimeout {
		poolCfg.ConnConfig.ConnectTimeout = commandConnectTimeout
	}
	return poolCfg, nil
}

func openCommandPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	poolCfg, err := commandPoolConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("could not connect to the database (connect timeout %s); nothing enqueued: %w", poolCfg.ConnConfig.ConnectTimeout, err)
	}
	return pool, nil
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
