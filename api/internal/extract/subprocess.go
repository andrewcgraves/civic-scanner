package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	BinaryName       = "extract"
	exitMalformed    = 10
	exitLimit        = 11
	exitNoText       = 12
	maxStderrBytes   = 4096
	subprocessWait   = 2 * time.Second
	defaultTimeout   = time.Minute
	defaultMaxMemory = 512 << 20
)

type Subprocess struct {
	Binary       string
	Timeout      time.Duration
	MaxMemory    int64
	MaxTextBytes int64
	Env          []string
}

func New(cfg config.Extract) *Extractor {
	return &Extractor{PDF: NewSubprocess(cfg)}
}

func NewSubprocess(cfg config.Extract) Subprocess {
	return Subprocess{
		Binary:       ResolveBinary(cfg.PDFBinary),
		Timeout:      cfg.PDFTimeout,
		MaxMemory:    cfg.PDFMaxMemory,
		MaxTextBytes: cfg.MaxTextBytes,
	}
}

func ResolveBinary(configured string) string {
	if configured != "" {
		return configured
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(self), BinaryName)
}

func (s Subprocess) withDefaults() Subprocess {
	if s.Timeout <= 0 {
		s.Timeout = defaultTimeout
	}
	if s.MaxMemory <= 0 {
		s.MaxMemory = defaultMaxMemory
	}
	if s.MaxTextBytes <= 0 {
		s.MaxTextBytes = DefaultLimits.MaxTextBytes
	}
	return s
}

func (s Subprocess) ReadPDF(ctx context.Context, body []byte) (Document, error) {
	s = s.withDefaults()
	if s.Binary == "" {
		return Document{}, fmt.Errorf("%w: no binary configured, set EXTRACT_PDF_BINARY", ErrUnavailable)
	}
	runCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	stdout := &cappedBuffer{max: int(s.MaxTextBytes), onOverflow: cancel}
	stderr := &cappedBuffer{max: maxStderrBytes, truncate: true}
	cmd := exec.CommandContext(runCtx, s.Binary, //nolint:gosec
		"-max-memory", strconv.FormatInt(s.MaxMemory, 10),
		"-max-text-bytes", strconv.FormatInt(s.MaxTextBytes, 10))
	cmd.Env = append(os.Environ(), s.Env...)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = subprocessWait
	err := cmd.Run()

	detail := strings.TrimSpace(stderr.String())
	switch {
	case ctx.Err() != nil:
		return Document{}, fmt.Errorf("pdf extraction: %w", ctx.Err())
	case stdout.overflowed():
		return Document{}, fmt.Errorf("%w: text exceeds %d bytes", ErrLimitExceeded, s.MaxTextBytes)
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return Document{}, fmt.Errorf("%w: pdf extraction took longer than %s", ErrLimitExceeded, s.Timeout)
	case err == nil:
		report, err := parseReport(stderr.String())
		if err != nil {
			return Document{}, err
		}
		return Document{Text: stdout.String(), Report: report}, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return Document{}, fmt.Errorf("%w: run %s: %w", ErrUnavailable, s.Binary, err)
	}
	switch exitErr.ExitCode() {
	case exitMalformed:
		return Document{}, childError(ErrMalformed, detail)
	case exitLimit:
		return Document{}, childError(ErrLimitExceeded, detail)
	case exitNoText:
		return Document{}, childError(ErrNoText, detail)
	}
	return Document{}, fmt.Errorf("%w: pdf extractor failed (%w): %s", ErrMalformed, exitErr, detail)
}

func parseReport(stderr string) (Report, error) {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	last := lines[len(lines)-1]
	encoded, ok := strings.CutPrefix(last, reportPrefix)
	var report Report
	if !ok || json.Unmarshal([]byte(encoded), &report) != nil {
		return Report{}, fmt.Errorf("%w: pdf extractor exited without a valid report: %q", ErrMalformed, truncateUTF8(last, maxReportedError))
	}
	return report, nil
}

func childError(sentinel error, detail string) error {
	return fmt.Errorf("%w: %s", sentinel, strings.TrimPrefix(detail, sentinel.Error()+": "))
}

type cappedBuffer struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	max        int
	truncate   bool
	over       bool
	onOverflow func()
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf.Len()+len(p) <= c.max {
		return c.buf.Write(p)
	}
	if c.truncate {
		c.buf.Write(p[:c.max-c.buf.Len()])
		return len(p), nil
	}
	c.over = true
	if c.onOverflow != nil {
		c.onOverflow()
	}
	return 0, ErrLimitExceeded
}

func (c *cappedBuffer) overflowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
