package summarize

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
)

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
