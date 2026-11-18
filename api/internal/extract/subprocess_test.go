package extract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract/extracttest"
	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
)

var (
	binaryOnce sync.Once
	binaryPath string
)

func extractBinary(t *testing.T) string {
	t.Helper()
	binaryOnce.Do(func() { binaryPath = testbin.BuildPackage(t, "../../cmd/extract") })
	if _, err := os.Stat(binaryPath); err != nil {
		binaryPath = testbin.BuildPackage(t, "../../cmd/extract")
	}
	return binaryPath
}

func subprocess(t *testing.T) Subprocess {
	t.Helper()
	return Subprocess{
		Binary:       extractBinary(t),
		Timeout:      20 * time.Second,
		MaxMemory:    256 << 20,
		MaxTextBytes: 1 << 20,
		Env:          testbin.CoverEnv(),
	}
}

func TestSubprocessExtractsAPDF(t *testing.T) {
	doc := extracttest.PDF(true, "BT /F1 10 Tf 72 720 Td [(Consent)-400(Calendar)] TJ ET")
	got, err := subprocess(t).ReadPDF(t.Context(), doc)
	if err != nil {
		t.Fatalf("ReadPDF: %v", err)
	}
	if Normalize(got) != "Consent Calendar" {
		t.Errorf("ReadPDF = %q", got)
	}
}

func TestSubprocessMapsChildFailures(t *testing.T) {
	cases := map[string]struct {
		doc  []byte
		want error
	}{
		"malformed": {[]byte("%PDF-1.4\ntruncated"), ErrMalformed},
		"no text":   {extracttest.PDF(false, "0 0 m 1 1 l S"), ErrNoText},
		"too many":  {extracttest.HugeCount(), ErrMalformed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := subprocess(t).ReadPDF(t.Context(), tc.doc)
			if !errors.Is(err, tc.want) {
				t.Errorf("ReadPDF err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestHostilePDFsFailWithinBoundedTimeAndMemory(t *testing.T) {
	cases := map[string]struct {
		doc  []byte
		want error
	}{
		"self-referencing kids": {extracttest.SelfReferencingPages(), ErrMalformed},
		"count of two billion":  {extracttest.HugeCount(), ErrMalformed},
		"flate bomb":            {flateBombPDF(t, 64<<20), ErrLimitExceeded},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sub := subprocess(t)
			sub.Timeout = 15 * time.Second
			start := time.Now()
			_, err := sub.ReadPDF(t.Context(), tc.doc)
			elapsed := time.Since(start)
			if !errors.Is(err, tc.want) {
				t.Errorf("ReadPDF err = %v, want %v", err, tc.want)
			}
			if elapsed > sub.Timeout+5*time.Second {
				t.Errorf("took %s, want under the timeout", elapsed)
			}
			t.Logf("%d bytes in, %v after %s", len(tc.doc), err, elapsed)
		})
	}
}

func TestSubprocessKillsAChildPastTheTimeout(t *testing.T) {
	hang := filepath.Join(t.TempDir(), "hang")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	sub := Subprocess{Binary: hang, Timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err := sub.ReadPDF(t.Context(), extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET"))
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s after a 300ms timeout", elapsed)
	}
}

func TestSubprocessCapsTheTextItReads(t *testing.T) {
	sub := subprocess(t)
	sub.MaxTextBytes = 4
	doc := extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Regular Board Meeting) Tj ET")
	if _, err := sub.ReadPDF(t.Context(), doc); !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("err = %v, want ErrLimitExceeded", err)
	}
}

func TestSubprocessReturnsTheCallersCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := subprocess(t).ReadPDF(ctx, extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSubprocessWithoutABinaryIsUnavailable(t *testing.T) {
	if _, err := (Subprocess{}).ReadPDF(t.Context(), []byte("%PDF-1.4")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no binary: err = %v", err)
	}
	missing := Subprocess{Binary: filepath.Join(t.TempDir(), "missing")}
	if _, err := missing.ReadPDF(t.Context(), []byte("%PDF-1.4")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("missing binary: err = %v", err)
	}
}

func TestSubprocessTreatsAnUnknownExitAsMalformed(t *testing.T) {
	_, err := Subprocess{Binary: "/usr/bin/false"}.ReadPDF(t.Context(), []byte("%PDF-1.4"))
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed", err)
	}
}

func TestNewResolvesTheBinaryNextToTheExecutable(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e := New(config.Extract{PDFTimeout: time.Second})
	sub, ok := e.PDF.(Subprocess)
	if !ok || sub.Binary != filepath.Join(filepath.Dir(self), BinaryName) || sub.Timeout != time.Second {
		t.Errorf("New = %+v", e.PDF)
	}
	if got := ResolveBinary("/opt/extract"); got != "/opt/extract" {
		t.Errorf("ResolveBinary = %q", got)
	}
}

func TestCappedBufferTruncatesOrOverflows(t *testing.T) {
	truncating := &cappedBuffer{max: 3, truncate: true}
	if n, err := truncating.Write([]byte("abcdef")); n != 6 || err != nil || truncating.String() != "abc" {
		t.Errorf("truncating = %d, %v, %q", n, err, truncating.String())
	}
	called := false
	strict := &cappedBuffer{max: 3, onOverflow: func() { called = true }}
	if _, err := strict.Write([]byte("abcd")); !errors.Is(err, ErrLimitExceeded) || !strict.overflowed() || !called {
		t.Errorf("strict = %v, %v, %v", err, strict.overflowed(), called)
	}
}

func TestRunChildInProcess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	doc := extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET")
	if code := RunChild(nil, bytes.NewReader(doc), &stdout, &stderr, func(int) {}); code != 0 || Normalize(stdout.String()) != "Agenda" {
		t.Errorf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}

	cases := []struct {
		name   string
		args   []string
		stdin  io.Reader
		stdout io.Writer
		want   int
	}{
		{"bad flag", []string{"-nope"}, bytes.NewReader(doc), io.Discard, exitUsage},
		{"zero memory", []string{"-max-memory", "0"}, bytes.NewReader(doc), io.Discard, exitUsage},
		{"unreadable stdin", nil, failingReader{}, io.Discard, exitMalformed},
		{"malformed", nil, strings.NewReader("%PDF-1.4\n"), io.Discard, exitMalformed},
		{"no text", nil, bytes.NewReader(extracttest.PDF(false, "0 0 m 1 1 l S")), io.Discard, exitNoText},
		{"too much text", []string{"-max-text-bytes", "2"}, bytes.NewReader(doc), io.Discard, exitLimit},
		{"closed stdout", nil, bytes.NewReader(doc), failingWriter{}, exitLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := RunChild(tc.args, tc.stdin, tc.stdout, io.Discard, func(int) {}); code != tc.want {
				t.Errorf("code = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestWatchMemoryReportsUseOverTheLimit(t *testing.T) {
	exceeded := make(chan uint64, 1)
	stop := watchMemory(1, time.Millisecond, func(used uint64) { exceeded <- used })
	defer stop()
	select {
	case used := <-exceeded:
		if used <= 1 {
			t.Errorf("used = %d", used)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watchMemory never reported")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }
