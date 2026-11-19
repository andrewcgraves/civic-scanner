package limit

import (
	"slices"
	"strings"
	"testing"
)

func noExec(t *testing.T) Execve {
	return func(string, []string, []string) error {
		t.Fatal("exec called")
		return nil
	}
}

func TestRunRejectsBadUsageWithoutTouchingLimits(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":    {"-unknown", "--", "true"},
		"no command":      {"-max-memory", "1", "-max-file-bytes", "1"},
		"no memory limit": {"-max-file-bytes", "1", "--", "true"},
		"no file limit":   {"-max-memory", "1", "--", "true"},
		"negative":        {"-max-memory", "-1", "-max-file-bytes", "1", "--", "true"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var stderr strings.Builder
			if code := Run(args, &stderr, noExec(t)); code != ExitUsage {
				t.Errorf("Run = %d, want %d (stderr %q)", code, ExitUsage, stderr.String())
			}
		})
	}
}

func TestRunReportsAMissingCommandBeforeSettingLimits(t *testing.T) {
	var stderr strings.Builder
	code := Run([]string{"-max-memory", "1", "-max-file-bytes", "1", "--", "/nonexistent/ffmpeg"}, &stderr, noExec(t))
	if code != ExitExec || !strings.Contains(stderr.String(), "/nonexistent/ffmpeg") {
		t.Errorf("Run = %d, stderr %q", code, stderr.String())
	}
}

func TestLimitsArgs(t *testing.T) {
	got := Limits{MaxMemory: 1 << 30, MaxFileBytes: 4096}.Args()
	want := []string{"-max-memory", "1073741824", "-max-file-bytes", "4096", "--"}
	if !slices.Equal(got, want) {
		t.Errorf("Args = %q, want %q", got, want)
	}
}
