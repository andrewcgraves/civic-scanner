//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type queuedCheck struct {
	id    int64
	args  jobs.CheckAgencyArgs
	state string
}

func uniqueName(prefix string) string {
	return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func checkQueue(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	queue := uniqueName("worker_check_test_")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM river_job WHERE queue = $1`, queue)
	})
	return queue
}

func queuedChecks(t *testing.T, pool *pgxpool.Pool, queue string) map[string]queuedCheck {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT id, args, state FROM river_job WHERE queue = $1 AND kind = $2`, queue, jobs.CheckAgencyArgs{}.Kind())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]queuedCheck{}
	for rows.Next() {
		var (
			c       queuedCheck
			encoded []byte
		)
		if err := rows.Scan(&c.id, &encoded, &c.state); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &c.args); err != nil {
			t.Fatal(err)
		}
		out[c.args.AgencySlug] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func runCheck(t *testing.T, queue string, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := checkAgency(t.Context(), args, &out, io.Discard, queue)
	return out.String(), err
}

func TestCheckAgencyCommandEnqueuesManualChecks(t *testing.T) {
	t.Setenv("DATABASE_URL", testdb.URL(t))
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("FEATURE_FLAGS", jobs.FlagCheckAgencies)
	pool := testdb.Pool(t)
	queue := checkQueue(t, pool)

	out, err := runCheck(t, queue, "la-metro")
	if err != nil {
		t.Fatalf("check-agency la-metro: %v", err)
	}
	queued := queuedChecks(t, pool, queue)
	first, ok := queued["la-metro"]
	if len(queued) != 1 || !ok || !first.args.Manual || first.state != "available" {
		t.Fatalf("queued checks = %+v, want one manual la-metro check", queued)
	}
	if want := "la-metro: enqueued manual check, job " + strconv.FormatInt(first.id, 10) + "\n"; !strings.HasPrefix(out, want) {
		t.Errorf("output = %q, want it to start with %q", out, want)
	}

	out, err = runCheck(t, queue, "--all")
	if err != nil {
		t.Fatalf("check-agency --all: %v", err)
	}
	queued = queuedChecks(t, pool, queue)
	for _, slug := range []string{"la-metro", "metrolink", "octa", "sbcta", "vctc"} {
		if c, ok := queued[slug]; !ok || !c.args.Manual {
			t.Errorf("no manual check queued for %s: %+v", slug, queued)
		}
	}
	if len(queued) != 5 {
		t.Errorf("queued checks = %d, want 5 (one per live agency with an adapter)", len(queued))
	}
	if queued["la-metro"].id != first.id {
		t.Errorf("la-metro was queued again while its manual check was waiting")
	}
	for _, want := range []string{
		"la-metro: not enqueued again, manual check job " + strconv.FormatInt(first.id, 10) + " is still available\n",
		"metrolink: enqueued manual check, job ",
		"rctc: skipped, no source adapter\n",
		"4 manual check(s) enqueued on queue \"" + queue + "\", 1 already waiting or running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--all output lacks %q:\n%s", want, out)
		}
	}
}

func TestCheckAgencyCommandRefusesAndEnqueuesNothing(t *testing.T) {
	t.Setenv("DATABASE_URL", testdb.URL(t))
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("FEATURE_FLAGS", jobs.FlagCheckAgencies)
	pool := testdb.Pool(t)
	queue := checkQueue(t, pool)
	pending := uniqueName("worker-test-pending-")
	if _, err := pool.Exec(t.Context(), `INSERT INTO agencies (slug, name, status, website) VALUES ($1, $1, 'pending', 'https://pending.example.com')`, pending); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM agencies WHERE slug = $1`, pending)
	})

	cases := map[string]struct {
		args []string
		want string
	}{
		"unknown slug":       {[]string{"no-such-agency"}, `agency "no-such-agency" is unknown or not live; nothing enqueued (live agencies with a source adapter: la-metro, metrolink, octa, sbcta, vctc)`},
		"pending agency":     {[]string{pending}, `agency "` + pending + `" is unknown or not live; nothing enqueued`},
		"live, no adapter":   {[]string{"rctc"}, `agency "rctc" is live but has no source adapter; nothing enqueued`},
		"two slugs at once":  {[]string{"la-metro", "octa"}, errCheckUsage.Error()},
		"unknown flag style": {[]string{"--octa"}, errCheckUsage.Error()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := runCheck(t, queue, tc.args...)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to start with %q", err, tc.want)
			}
			if out != "" {
				t.Errorf("output = %q, want none", out)
			}
		})
	}

	t.Run("flag off", func(t *testing.T) {
		t.Setenv("FEATURE_FLAGS", "")
		if _, err := runCheck(t, queue, "--all"); !errors.Is(err, errCheckFlagOff) {
			t.Errorf("error = %v, want %v", err, errCheckFlagOff)
		}
	})

	t.Run("flag provider failing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("FEATURE_FLAGS", jobs.FlagCheckAgencies)
		t.Setenv("POSTHOG_API_KEY", "phc_test")
		t.Setenv("POSTHOG_HOST", srv.URL)
		var stdout, stderr strings.Builder
		err := checkAgency(t.Context(), []string{"--all"}, &stdout, &stderr, queue)
		if !errors.Is(err, errCheckFlagOff) || !strings.Contains(err.Error(), "is off or could not be evaluated (see any warnings above on stderr)") {
			t.Errorf("error = %v, want %v", err, errCheckFlagOff)
		}
		if !strings.Contains(stderr.String(), `"level":"WARN"`) || stdout.String() != "" {
			t.Errorf("stderr = %q, stdout = %q; want a warning on stderr and nothing on stdout", stderr.String(), stdout.String())
		}
	})

	if queued := queuedChecks(t, pool, queue); len(queued) != 0 {
		t.Errorf("queued checks after refusals = %+v, want none", queued)
	}
}

func TestCheckAgencyCommandRefusesAnUnmigratedDatabase(t *testing.T) {
	pool := testdb.Pool(t)
	schema := uniqueName("worker_check_empty_")
	if _, err := pool.Exec(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DROP SCHEMA `+schema)
	})
	t.Setenv("DATABASE_URL", testdb.URL(t)+"&search_path="+schema)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("FEATURE_FLAGS", jobs.FlagCheckAgencies)

	if _, err := runCheck(t, checkQueue(t, pool), "--all"); !errors.Is(err, errNotMigrated) {
		t.Errorf("error = %v, want %v", err, errNotMigrated)
	}
}

func TestBinaryCheckAgencyRejectsAnUnknownSlug(t *testing.T) {
	testdb.Pool(t)
	cmd := testbin.Command(t, testbin.Build(t), []string{"DATABASE_URL=" + testdb.URL(t), "LOG_LEVEL=error", "FEATURE_FLAGS=" + jobs.FlagCheckAgencies},
		"check-agency", "no-such-agency")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if code := testbin.ExitCode(t, cmd.Run()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := `worker: agency "no-such-agency" is unknown or not live; nothing enqueued`; !strings.HasPrefix(stderr.String(), want) || stdout.String() != "" {
		t.Errorf("stderr = %q, stdout = %q; want stderr starting %q", stderr.String(), stdout.String(), want)
	}
}
