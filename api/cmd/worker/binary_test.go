package main

import (
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
)

func TestBinaryExitsWithErrorOnBadConfig(t *testing.T) {
	cmd := testbin.Command(t, testbin.Build(t), []string{"DATABASE_URL="})
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if code := testbin.ExitCode(t, cmd.Run()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "worker: DATABASE_URL is required\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestBinaryHelpExitsZero(t *testing.T) {
	bin := testbin.Build(t)
	for args, want := range map[string]string{"--help": workerHelp, "check-agency -h": checkHelp, "set-role --help": setRoleHelp} {
		cmd := testbin.Command(t, bin, []string{"DATABASE_URL="}, strings.Fields(args)...)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if code := testbin.ExitCode(t, cmd.Run()); code != 0 {
			t.Errorf("worker %s: exit code = %d, want 0", args, code)
		}
		if stdout.String() != want || stderr.String() != "" {
			t.Errorf("worker %s: stdout = %q, stderr = %q; want stdout %q", args, stdout.String(), stderr.String(), want)
		}
	}
}

func TestBinaryCheckAgencyExitsWithAnError(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"missing DATABASE_URL":          {[]string{"check-agency", "la-metro"}, "worker: DATABASE_URL is required\n"},
		"no target":                     {[]string{"check-agency"}, "worker: usage: worker check-agency (<agency-slug> | --all)\n"},
		"unknown command":               {[]string{"run"}, "worker: usage: worker [check-agency (<agency-slug> | --all) | set-role (<user-id> | <email>) (user | admin)]\n"},
		"set-role without DATABASE_URL": {[]string{"set-role", "ada@example.com", "admin"}, "worker: DATABASE_URL is required\n"},
		"set-role with a bad role":      {[]string{"set-role", "ada@example.com", "root"}, "worker: usage: worker set-role (<user-id> | <email>) (user | admin): invalid role: \"root\", want \"user\" or \"admin\"\n"},
	}
	bin := testbin.Build(t)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := testbin.Command(t, bin, []string{"DATABASE_URL="}, tc.args...)
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if code := testbin.ExitCode(t, cmd.Run()); code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if stderr.String() != tc.want || stdout.String() != "" {
				t.Errorf("stderr = %q, stdout = %q; want stderr %q", stderr.String(), stdout.String(), tc.want)
			}
		})
	}
}
