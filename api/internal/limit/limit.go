package limit

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

const (
	BinaryName = "limit"
	ExitUsage  = 2
	ExitLimits = 125
	ExitExec   = 127
)

type Execve func(path string, argv []string, env []string) error

type Limits struct {
	MaxMemory    int64
	MaxFileBytes int64
}

func (l Limits) Args() []string {
	return []string{"-max-memory", fmt.Sprint(l.MaxMemory), "-max-file-bytes", fmt.Sprint(l.MaxFileBytes), "--"}
}

func Run(args []string, stderr io.Writer, execve Execve) int {
	flags := flag.NewFlagSet(BinaryName, flag.ContinueOnError)
	flags.SetOutput(stderr)
	maxMemory := flags.Int64("max-memory", 0, "ceiling on the command's data segment and writable mappings, in bytes")
	maxFile := flags.Int64("max-file-bytes", 0, "largest file the command may write, in bytes")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	command := flags.Args()
	if *maxMemory <= 0 || *maxFile <= 0 || len(command) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: limit -max-memory BYTES -max-file-bytes BYTES -- COMMAND [ARG...]")
		return ExitUsage
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "limit: %v\n", err)
		return ExitExec
	}
	if err := applyMemory(*maxMemory); err != nil {
		_, _ = fmt.Fprintf(stderr, "limit: set memory limit: %v\n", err)
		return ExitLimits
	}
	if err := apply(syscall.RLIMIT_FSIZE, *maxFile); err != nil {
		_, _ = fmt.Fprintf(stderr, "limit: set file size limit: %v\n", err)
		return ExitLimits
	}
	if err := execve(path, command, os.Environ()); err != nil {
		_, _ = fmt.Fprintf(stderr, "limit: exec %s: %v\n", path, err)
	}
	return ExitExec
}

func applyMemory(value int64) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	return apply(syscall.RLIMIT_DATA, value)
}

func apply(resource int, value int64) error {
	v := uint64(value) //nolint:gosec
	return syscall.Setrlimit(resource, &syscall.Rlimit{Cur: v, Max: v})
}
