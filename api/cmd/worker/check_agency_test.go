package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

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
	live := agencies("vctc", "rctc", "la-metro", "octa")
	withAdapters := []string{"la-metro", "metrolink", "octa", "vctc"}

	plan, err := planChecks(checkTarget{all: true}, live, withAdapters)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"la-metro", "octa", "vctc"}; !slices.Equal(plan.slugs, want) {
		t.Errorf("--all slugs = %v, want %v (live with an adapter; metrolink is not live)", plan.slugs, want)
	}
	if want := []string{"rctc"}; !slices.Equal(plan.noAdapter, want) {
		t.Errorf("--all skipped = %v, want %v", plan.noAdapter, want)
	}

	plan, err = planChecks(checkTarget{slug: "octa"}, live, withAdapters)
	if err != nil || !slices.Equal(plan.slugs, []string{"octa"}) {
		t.Errorf("octa plan = %+v, %v", plan, err)
	}

	for slug, want := range map[string]string{
		"metrolink": `agency "metrolink" is unknown or not live; nothing enqueued (live agencies with a source adapter: la-metro, octa, vctc)`,
		"nope":      `agency "nope" is unknown or not live; nothing enqueued (live agencies with a source adapter: la-metro, octa, vctc)`,
		"rctc":      `agency "rctc" is live but has no source adapter; nothing enqueued`,
	} {
		if _, err := planChecks(checkTarget{slug: slug}, live, withAdapters); err == nil || err.Error() != want {
			t.Errorf("planChecks(%s) error = %v, want %q", slug, err, want)
		}
	}

	if _, err := planChecks(checkTarget{all: true}, agencies("rctc"), withAdapters); !errors.Is(err, errNothingToDo) {
		t.Errorf("--all with nothing checkable: error = %v", err)
	}
	if _, err := planChecks(checkTarget{slug: "octa"}, nil, withAdapters); err == nil || !strings.Contains(err.Error(), "adapter: none)") {
		t.Errorf("no live agencies: error = %v", err)
	}
}

func TestAdapterSlugsAreTheRegisteredAdapters(t *testing.T) {
	if got, want := adapterSlugs(), []string{"la-metro", "metrolink", "octa", "vctc"}; !slices.Equal(got, want) {
		t.Errorf("adapterSlugs() = %v, want %v", got, want)
	}
}

func TestReport(t *testing.T) {
	var b strings.Builder
	checks := []jobs.ManualCheck{
		{AgencySlug: "la-metro", JobID: 12},
		{AgencySlug: "octa", JobID: 9, State: rivertype.JobStateRunning, Duplicate: true},
	}
	if err := report(&b, "default", checks, []string{"rctc"}, true); err != nil {
		t.Fatal(err)
	}
	want := "la-metro: enqueued manual check, job 12\n" +
		"octa: not enqueued again, manual check job 9 is still running\n" +
		"rctc: skipped, no source adapter\n" +
		"1 manual check(s) enqueued on queue \"default\", 1 already waiting or running; the worker logs \"agency check triggered manually\" when each one starts\n"
	if b.String() != want {
		t.Errorf("report =\n%s\nwant\n%s", b.String(), want)
	}
}

func TestDispatchRejectsUnknownCommands(t *testing.T) {
	if err := dispatch(t.Context(), []string{"check"}, nil); !errors.Is(err, errWorkerUsage) {
		t.Errorf("dispatch(check) = %v, want the usage error", err)
	}
	if err := dispatch(t.Context(), []string{checkAgencyCommand}, nil); !errors.Is(err, errCheckUsage) {
		t.Errorf("dispatch(check-agency) = %v, want the check usage error", err)
	}
}

func TestCheckAgencyRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	err := dispatch(t.Context(), []string{checkAgencyCommand, "la-metro"}, nil)
	if err == nil || err.Error() != "DATABASE_URL is required" {
		t.Errorf("error = %v, want DATABASE_URL is required", err)
	}
}

func TestCheckAgencyFailsWhenDatabaseUnreachable(t *testing.T) {
	t.Setenv("DATABASE_URL", unreachableDatabaseURL)
	if err := dispatch(t.Context(), []string{checkAgencyCommand, "--all"}, nil); err == nil || !strings.Contains(err.Error(), "ping database") {
		t.Errorf("error = %v, want a ping failure", err)
	}
}
