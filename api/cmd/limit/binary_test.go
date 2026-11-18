package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
)

func TestBinaryAppliesLimitsAndExecsTheCommand(t *testing.T) {
	cmd := testbin.Command(t, testbin.Build(t), nil,
		"-max-memory", "268435456", "-max-file-bytes", "1048576", "--", "sh", "-c", "ulimit -d; echo ran")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	wantData := "262144"
	if runtime.GOOS != "linux" {
		wantData = "unlimited"
	}
	lines := strings.Fields(string(out))
	if len(lines) != 2 || lines[0] != wantData || lines[1] != "ran" {
		t.Errorf("output = %q, want the data limit (%s KiB), then ran", out, wantData)
	}
}

func TestBinaryKillsACommandThatWritesPastTheFileLimit(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out")
	cmd := testbin.Command(t, testbin.Build(t), nil,
		"-max-memory", "268435456", "-max-file-bytes", "4096", "--", "dd", "if=/dev/zero", "of="+dst, "bs=1024", "count=64")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run = %v, want the command to be stopped", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGXFSZ {
		t.Errorf("status = %v, want killed by SIGXFSZ", exitErr)
	}
	if info, err := os.Stat(dst); err != nil || info.Size() > 4096 {
		t.Errorf("output = %v, %v; want at most 4096 bytes", info, err)
	}
}

func TestBinaryExitCodes(t *testing.T) {
	bin := testbin.Build(t)
	cases := map[string]struct {
		args []string
		want int
	}{
		"usage":           {[]string{"--", "true"}, 2},
		"missing command": {[]string{"-max-memory", "1048576", "-max-file-bytes", "1", "--", "/nonexistent/ffmpeg"}, 127},
		"command exit":    {[]string{"-max-memory", "268435456", "-max-file-bytes", "1", "--", "sh", "-c", "exit 7"}, 7},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := testbin.Command(t, bin, nil, tc.args...)
			if code := testbin.ExitCode(t, cmd.Run()); code != tc.want {
				t.Errorf("exit code = %d, want %d", code, tc.want)
			}
		})
	}
}
