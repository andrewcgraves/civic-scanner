package testbin

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	CoverPkgEnv = "TESTBIN_COVERPKG"
	CoverDirEnv = "TESTBIN_COVERDIR"
)

func Build(t testing.TB) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	args := []string{"build", "-o", bin}
	pkgs, dir := os.Getenv(CoverPkgEnv), os.Getenv(CoverDirEnv)
	if pkgs != "" && dir != "" {
		args = append(args, "-cover", "-covermode=atomic", "-coverpkg="+pkgs)
	}
	out, err := exec.Command("go", append(args, ".")...).CombinedOutput() //nolint:gosec
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func Command(t testing.TB, bin string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, args...) //nolint:gosec
	cmd.Env = append(os.Environ(), env...)
	if dir := os.Getenv(CoverDirEnv); dir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func ExitCode(t testing.TB, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run binary: %v", err)
	}
	return exitErr.ExitCode()
}

type outputWatcher struct {
	want []byte
	buf  bytes.Buffer
	once sync.Once
	seen chan struct{}
}

func (w *outputWatcher) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if bytes.Contains(w.buf.Bytes(), w.want) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

func WatchStdout(cmd *exec.Cmd, want string) <-chan struct{} {
	w := &outputWatcher{want: []byte(want), seen: make(chan struct{})}
	cmd.Stdout = w
	return w.seen
}

func Start(t testing.TB, cmd *exec.Cmd) <-chan error {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done
}
