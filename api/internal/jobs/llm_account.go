package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
)

const (
	LLMAccountFailureMessage = "llm account or configuration failure"
	llmAccountCooldown       = 10 * time.Minute
	llmAccountSnooze         = time.Hour
	llmPausedMinSnooze       = time.Minute
)

type llmPausedError struct {
	wait time.Duration
}

func (e *llmPausedError) Error() string {
	return fmt.Sprintf("llm calls paused for %s after an account or configuration failure", e.wait)
}

type llmBreaker struct {
	clock    sources.Clock
	cooldown time.Duration

	mu         sync.Mutex
	openUntil  time.Time
	probing    bool
	probeUntil time.Time
}

func newLLMBreaker(clock sources.Clock, cooldown time.Duration) *llmBreaker {
	return &llmBreaker{clock: clock, cooldown: cooldown}
}

func (b *llmBreaker) allow() (bool, time.Duration) {
	now := b.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Before(b.openUntil) {
		return false, b.openUntil.Sub(now)
	}
	if b.probing && now.Before(b.probeUntil) {
		return false, b.probeUntil.Sub(now)
	}
	if !b.openUntil.IsZero() {
		b.probing = true
		b.probeUntil = now.Add(b.cooldown)
	}
	return true, 0
}

func (b *llmBreaker) record(err error) {
	now := b.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	if llm.IsAccountFailure(err) {
		b.openUntil = now.Add(b.cooldown)
		return
	}
	b.openUntil = time.Time{}
}

type guardedLLM struct {
	client  llm.Client
	breaker *llmBreaker
}

func guardLLM(client llm.Client, breaker *llmBreaker) llm.Client {
	if client == nil {
		return nil
	}
	return guardedLLM{client: client, breaker: breaker}
}

func (g guardedLLM) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if ok, wait := g.breaker.allow(); !ok {
		return llm.Response{}, &llmPausedError{wait: wait}
	}
	resp, err := g.client.Complete(ctx, req)
	g.breaker.record(err)
	return resp, err
}

func llmAccountSnoozeFor(ctx context.Context, logger *slog.Logger, err error) error {
	var paused *llmPausedError
	if errors.As(err, &paused) {
		wait := max(paused.wait, llmPausedMinSnooze)
		logger.InfoContext(ctx, "llm call skipped after an account or configuration failure", "snooze", wait)
		return river.JobSnooze(wait)
	}
	if !logLLMAccountFailure(ctx, logger, err, "snooze", llmAccountSnooze) {
		return nil
	}
	return river.JobSnooze(llmAccountSnooze)
}

func logLLMAccountFailure(ctx context.Context, logger *slog.Logger, err error, attrs ...any) bool {
	var e *llm.Error
	if !errors.As(err, &e) || !e.Account {
		return false
	}
	logger.ErrorContext(ctx, LLMAccountFailureMessage, append([]any{
		"provider", e.Provider, "model", e.Model, "status", e.StatusCode, "error_type", e.Type, "error_code", e.Code,
	}, attrs...)...)
	return true
}

func isLLMPaused(err error) bool {
	var paused *llmPausedError
	return errors.As(err, &paused)
}
