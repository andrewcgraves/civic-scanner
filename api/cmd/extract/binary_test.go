package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract/extracttest"
	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
)

func TestBinaryExtractsTextFromStdin(t *testing.T) {
	cmd := testbin.Command(t, testbin.Build(t), nil)
	cmd.Stdin = bytes.NewReader(extracttest.Lines("Regular Board Meeting", "1. Call to Order"))
	var stdout strings.Builder
	cmd.Stdout = &stdout

	if code := testbin.ExitCode(t, cmd.Run()); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := stdout.String(); got != "Regular Board Meeting\n1. Call to Order\n" {
		t.Errorf("stdout = %q", got)
	}
}

func TestBinaryExitCodes(t *testing.T) {
	bin := testbin.Build(t)
	cases := map[string]struct {
		args  []string
		stdin []byte
		want  int
	}{
		"bad flag":  {[]string{"-unknown"}, nil, 2},
		"malformed": {nil, []byte("%PDF-1.4\ntruncated"), 10},
		"too long":  {[]string{"-max-text-bytes", "3"}, extracttest.Lines("Regular Board Meeting"), 11},
		"no text":   {nil, extracttest.PDF(false, "0 0 m 1 1 l S"), 12},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := testbin.Command(t, bin, nil, tc.args...)
			cmd.Stdin = bytes.NewReader(tc.stdin)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			if code := testbin.ExitCode(t, cmd.Run()); code != tc.want {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tc.want, stderr.String())
			}
		})
	}
}
