package summarize

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
)

func TestAChangeRequestWithoutADelimiterIsAnError(t *testing.T) {
	diff := diffOf(t, "1. Call to Order", "1. Call to Order\n2. Roll Call")
	if _, _, err := changeRequest(llm.TierRoutine, diff, bytes.NewReader(nil)); err == nil {
		t.Error("err = nil, want the read error")
	}
	old := randomSource
	randomSource = bytes.NewReader(nil)
	t.Cleanup(func() { randomSource = old })
	fake := llmtest.Returning("never")
	if _, err := AgendaChange(context.Background(), fake, llm.TierRoutine, diff); err == nil || len(fake.Calls()) != 0 {
		t.Errorf("err = %v, calls = %d", err, len(fake.Calls()))
	}
}

func TestATranscriptRequestWithoutADelimiterCallsNoModel(t *testing.T) {
	speech := strings.Repeat("The board votes on the budget. ", 10)
	long := make([]TranscriptSegment, 2500)
	for i := range long {
		long[i] = TranscriptSegment{Text: strings.Repeat("word ", 24)}
	}
	for name, segments := range map[string][]TranscriptSegment{"one part": {{Text: speech}}, "several parts": long} {
		fake := llmtest.Returning("never")
		if _, err := transcript(context.Background(), fake, llm.TierRoutine, segments, "", bytes.NewReader(nil)); err == nil || len(fake.Calls()) != 0 {
			t.Errorf("%s: err = %v, calls = %d", name, err, len(fake.Calls()))
		}
	}
	parts := SplitTranscript(long, "")
	if _, err := combineRequest(llm.TierRoutine, parts, make([]string, len(parts)), bytes.NewReader(nil)); err == nil {
		t.Error("combine request without a delimiter: err = nil")
	}
	if got := clock(-time.Second); got != "0:00:00" {
		t.Errorf("clock(-1s) = %q", got)
	}
	if got := clock(4*time.Hour + 5*time.Minute + 6*time.Second); got != "4:05:06" {
		t.Errorf("clock = %q", got)
	}
}

func TestADelimiterFoundInTheDocumentIsDrawnAgain(t *testing.T) {
	taken, fresh := bytes.Repeat([]byte{0xab}, delimiterBytes), bytes.Repeat([]byte{0x01}, delimiterBytes)
	text := "Agenda\nItem " + strings.ToUpper(strings.Repeat("ab", delimiterBytes)) + "\nEnd"

	req, input, err := agendaRequest(llm.TierRoutine, text, bytes.NewReader(append(taken, fresh...)))
	if err != nil {
		t.Fatalf("agendaRequest: %v", err)
	}
	if input.Delimiter != strings.Repeat("01", delimiterBytes) || !strings.Contains(req.System, input.Delimiter) {
		t.Errorf("delimiter = %q, want the second draw", input.Delimiter)
	}
}

func TestNoDelimiterAfterEveryDrawCollides(t *testing.T) {
	taken := bytes.Repeat([]byte{0xab}, delimiterBytes*delimiterAttempts)
	text := strings.Repeat("ab", delimiterBytes)

	if _, _, err := agendaRequest(llm.TierRoutine, text, bytes.NewReader(taken)); !errors.Is(err, ErrNoDelimiter) {
		t.Errorf("err = %v, want ErrNoDelimiter", err)
	}
}

func TestAFailingRandomSourceIsAnError(t *testing.T) {
	if _, _, err := agendaRequest(llm.TierRoutine, "agenda", bytes.NewReader(nil)); err == nil || errors.Is(err, ErrNoDelimiter) {
		t.Errorf("err = %v, want the read error", err)
	}
}

func TestAgendaDoesNotCallTheModelWithoutADelimiter(t *testing.T) {
	fake := llmtest.Returning("never")
	old := randomSource
	randomSource = bytes.NewReader(nil)
	t.Cleanup(func() { randomSource = old })

	if _, err := Agenda(context.Background(), fake, llm.TierRoutine, "agenda"); err == nil || len(fake.Calls()) != 0 {
		t.Errorf("err = %v, calls = %d", err, len(fake.Calls()))
	}
}
