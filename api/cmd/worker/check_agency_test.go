package main

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

func TestParseCheckArgs(t *testing.T) {
	cases := []struct {
		args []string
		want checkTarget
		err  bool
	}{
		{args: []string{"la-metro"}, want: checkTarget{slug: "la-metro"}},
		{args: []string{"--all"}, want: checkTarget{all: true}},
		{args: nil, err: true},
		{args: []string{""}, err: true},
		{args: []string{"--everything"}, err: true},
		{args: []string{"la-metro", "octa"}, err: true},
		{args: []string{"--all", "la-metro"}, err: true},
	}
	for _, tc := range cases {
		got, err := parseCheckArgs(tc.args)
		if tc.err {
			if !errors.Is(err, errCheckUsage) {
				t.Errorf("parseCheckArgs(%q) error = %v, want the usage error", tc.args, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseCheckArgs(%q) = %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
}

func agencies(slugs ...string) []store.Agency {
	out := make([]store.Agency, 0, len(slugs))
	for _, s := range slugs {
		out = append(out, store.Agency{Slug: s, Name: s})
	}
	return out
}

func TestPlanChecks(t *testing.T) {
	live := agencies("vctc", "no-adapter", "la-metro", "octa")
	withAdapters := []string{"la-metro", "metrolink", "octa", "vctc"}

	plan, err := planChecks(checkTarget{all: true}, live, withAdapters)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"la-metro", "octa", "vctc"}; !slices.Equal(plan.slugs, want) {
		t.Errorf("--all slugs = %v, want %v (live with an adapter; metrolink is not live)", plan.slugs, want)
	}
	if want := []string{"no-adapter"}; !slices.Equal(plan.noAdapter, want) {
		t.Errorf("--all skipped = %v, want %v", plan.noAdapter, want)
	}

	plan, err = planChecks(checkTarget{slug: "octa"}, live, withAdapters)
	if err != nil || !slices.Equal(plan.slugs, []string{"octa"}) {
		t.Errorf("octa plan = %+v, %v", plan, err)
	}

	for slug, want := range map[string]string{
		"metrolink":  `agency "metrolink" is unknown or not live; nothing enqueued (live agencies with a source adapter: la-metro, octa, vctc)`,
		"nope":       `agency "nope" is unknown or not live; nothing enqueued (live agencies with a source adapter: la-metro, octa, vctc)`,
		"no-adapter": `agency "no-adapter" is live but has no source adapter; nothing enqueued`,
	} {
		if _, err := planChecks(checkTarget{slug: slug}, live, withAdapters); err == nil || err.Error() != want {
			t.Errorf("planChecks(%s) error = %v, want %q", slug, err, want)
		}
	}

	if _, err := planChecks(checkTarget{all: true}, agencies("no-adapter"), withAdapters); !errors.Is(err, errNothingToDo) {
		t.Errorf("--all with nothing checkable: error = %v", err)
	}
	if _, err := planChecks(checkTarget{slug: "octa"}, nil, withAdapters); err == nil || !strings.Contains(err.Error(), "adapter: none)") {
		t.Errorf("no live agencies: error = %v", err)
	}
}

func TestAdapterSlugsAreTheRegisteredAdapters(t *testing.T) {
	got := adapterSlugs()
	if !slices.IsSorted(got) || len(got) != len(adapters) {
		t.Errorf("adapterSlugs() = %v, want every registered slug once, sorted", got)
	}
	for slug := range adapters {
		if !slices.Contains(got, slug) {
			t.Errorf("adapterSlugs() = %v, lacks %s", got, slug)
		}
	}
}

func TestReport(t *testing.T) {
	var b strings.Builder
	checks := []jobs.ManualCheck{
		{AgencySlug: "la-metro", JobID: 12},
		{AgencySlug: "octa", JobID: 9, State: rivertype.JobStateRunning, Duplicate: true},
	}
	if err := report(&b, "default", checks, []string{"no-adapter"}, true); err != nil {
		t.Fatal(err)
	}
	want := "la-metro: enqueued manual check, job 12\n" +
		"octa: not enqueued again, manual check job 9 is still running\n" +
		"no-adapter: skipped, no source adapter\n" +
		"1 manual check(s) enqueued on queue \"default\", 1 already waiting or running; the worker logs \"agency check triggered manually\" when each one starts\n"
	if b.String() != want {
		t.Errorf("report =\n%s\nwant\n%s", b.String(), want)
	}
}

func TestDispatchRejectsUnknownCommands(t *testing.T) {
	if err := dispatch(t.Context(), []string{"check"}, io.Discard, io.Discard); !errors.Is(err, errWorkerUsage) {
		t.Errorf("dispatch(check) = %v, want the usage error", err)
	}
	if err := dispatch(t.Context(), []string{checkAgencyCommand}, io.Discard, io.Discard); !errors.Is(err, errCheckUsage) {
		t.Errorf("dispatch(check-agency) = %v, want the check usage error", err)
	}
}

func TestCheckAgencyRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	err := dispatch(t.Context(), []string{checkAgencyCommand, "la-metro"}, io.Discard, io.Discard)
	if err == nil || err.Error() != "DATABASE_URL is required" {
		t.Errorf("error = %v, want DATABASE_URL is required", err)
	}
}

func TestCheckAgencyFailsWhenDatabaseUnreachable(t *testing.T) {
	t.Setenv("DATABASE_URL", unreachableDatabaseURL)
	err := dispatch(t.Context(), []string{checkAgencyCommand, "--all"}, io.Discard, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "could not connect to the database (connect timeout 1s); nothing enqueued: ") {
		t.Errorf("error = %v, want a connect failure naming the timeout", err)
	}
}

func TestCommandPoolConfigBoundsTheConnectTimeout(t *testing.T) {
	cases := map[string]time.Duration{
		"postgres://u:p@db.internal:5432/civic":                    10 * time.Second,
		"postgres://u:p@db.internal:5432/civic?connect_timeout=3":  3 * time.Second,
		"postgres://u:p@db.internal:5432/civic?connect_timeout=75": 10 * time.Second,
		"postgres://u:p@db.internal:5432/civic?connect_timeout=0":  10 * time.Second,
	}
	for url, want := range cases {
		cfg, err := commandPoolConfig(url)
		if err != nil {
			t.Fatalf("commandPoolConfig(%s): %v", url, err)
		}
		if got := cfg.ConnConfig.ConnectTimeout; got != want {
			t.Errorf("connect timeout for %s = %v, want %v", url, got, want)
		}
	}
	if _, err := commandPoolConfig("postgres://u:p@db.internal:notaport/civic"); err == nil || !strings.HasPrefix(err.Error(), "parse DATABASE_URL: ") {
		t.Errorf("bad URL error = %v", err)
	}
}

func TestHelpPrintsUsageAndSucceeds(t *testing.T) {
	cases := map[string]string{}
	for _, h := range []string{"-h", "--help", "help"} {
		cases[h] = workerHelp
		cases[checkAgencyCommand+" "+h] = checkHelp
	}
	t.Setenv("DATABASE_URL", "")
	for args, want := range cases {
		var out strings.Builder
		if err := dispatch(t.Context(), strings.Fields(args), &out, io.Discard); err != nil {
			t.Errorf("dispatch(%s) = %v, want nil", args, err)
		}
		if out.String() != want {
			t.Errorf("dispatch(%s) printed %q, want %q", args, out.String(), want)
		}
	}
	for _, args := range []string{"help extra", "check-agency --help extra"} {
		if err := dispatch(t.Context(), strings.Fields(args), io.Discard, io.Discard); err == nil {
			t.Errorf("dispatch(%s) succeeded, want a usage error", args)
		}
	}
}
