package extract

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

const (
	memoryCheckInterval = 2 * time.Millisecond
	memoryHeadroom      = 2
	exitUsage           = 2
	metricTotal         = "/memory/classes/total:bytes"
	metricReleased      = "/memory/classes/heap/released:bytes"
	reportPrefix        = "extract report: "
)

func RunChild(args []string, stdin io.Reader, stdout, stderr io.Writer, exit func(int)) int {
	flags := flag.NewFlagSet("extract", flag.ContinueOnError)
	flags.SetOutput(stderr)
	maxMemory := flags.Int64("max-memory", defaultMaxMemory, "memory ceiling in bytes")
	maxText := flags.Int64("max-text-bytes", DefaultLimits.MaxTextBytes, "largest text output in bytes")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if *maxMemory <= 0 || *maxText <= 0 {
		_, _ = fmt.Fprintln(stderr, "extract: -max-memory and -max-text-bytes must be positive")
		return exitUsage
	}

	trip := *maxMemory / memoryHeadroom
	debug.SetMemoryLimit(trip)
	stop := watchMemory(trip, memoryCheckInterval, func(used uint64) {
		_, _ = fmt.Fprintf(stderr, "memory use %d bytes passed %d, half the %d byte ceiling\n", used, trip, *maxMemory)
		exit(exitLimit)
	})
	defer stop()

	body, err := io.ReadAll(stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "read pdf: %v\n", err)
		return exitMalformed
	}
	doc, err := ReadPDF(context.Background(), body, Limits{MaxTextBytes: *maxText})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitCode(err)
	}
	if _, err := io.WriteString(stdout, doc.Text); err != nil {
		_, _ = fmt.Fprintf(stderr, "write text: %v\n", err)
		return exitLimit
	}
	report, _ := json.Marshal(doc.Report)
	_, _ = fmt.Fprintf(stderr, "%s%s\n", reportPrefix, report)
	return 0
}

func exitCode(err error) int {
	switch {
	case errors.Is(err, ErrLimitExceeded):
		return exitLimit
	case errors.Is(err, ErrNoText):
		return exitNoText
	}
	return exitMalformed
}

func watchMemory(limit int64, every time.Duration, exceeded func(used uint64)) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	samples := []metrics.Sample{{Name: metricTotal}, {Name: metricReleased}}
	go func() {
		defer close(finished)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				metrics.Read(samples)
				used := samples[0].Value.Uint64() - samples[1].Value.Uint64()
				if used > uint64(limit) { //nolint:gosec
					exceeded(used)
					return
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
