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
	key  string
	wait time.Duration
}

func (e *llmPausedError) Error() string {
	return fmt.Sprintf("llm calls to %s paused for %s after an account or configuration failure", e.key, e.wait)
}

type llmBreaker struct {
	clock    sources.Clock
	cooldown time.Duration

	mu    sync.Mutex
	state map[string]*breakerState
}

type breakerState struct {
	epoch      uint64
	openUntil  time.Time
	probing    bool
	probeUntil time.Time
}

type admission struct {
	key   string
	epoch uint64
	probe bool
}

func newLLMBreaker(clock sources.Clock, cooldown time.Duration) *llmBreaker {
	return &llmBreaker{clock: clock, cooldown: cooldown, state: map[string]*breakerState{}}
}

func (b *llmBreaker) stateOf(key string) *breakerState {
	s, ok := b.state[key]
	if !ok {
		s = &breakerState{}
		b.state[key] = s
	}
	return s
}

func (b *llmBreaker) allow(key string) (admission, bool, time.Duration) {
	now := b.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stateOf(key)
	if now.Before(s.openUntil) {
		return admission{}, false, s.openUntil.Sub(now)
	}
	if s.probing && now.Before(s.probeUntil) {
		return admission{}, false, s.probeUntil.Sub(now)
	}
	if s.openUntil.IsZero() {
		return admission{key: key, epoch: s.epoch}, true, 0
	}
	s.epoch++
	s.probing = true
	s.probeUntil = now.Add(b.cooldown)
	return admission{key: key, epoch: s.epoch, probe: true}, true, 0
}

func (b *llmBreaker) record(a admission, err error) {
	now := b.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stateOf(a.key)
	if llm.IsAccountFailure(err) {
		s.epoch++
		s.openUntil = now.Add(b.cooldown)
		s.probing = false
		return
	}
	if a.probe && a.epoch == s.epoch {
		s.openUntil = time.Time{}
		s.probing = false
	}
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

func (g guardedLLM) key(tier llm.Tier) string {
	if r, ok := g.client.(llm.Router); ok {
		if provider, model := r.Route(tier); model != "" {
			return provider + " " + model
		}
	}
	return "tier " + string(tier)
}

func (g guardedLLM) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	key := g.key(req.Tier)
	a, ok, wait := g.breaker.allow(key)
	if !ok {
		return llm.Response{}, &llmPausedError{key: key, wait: wait}
	}
	resp, err := g.client.Complete(ctx, req)
	g.breaker.record(a, err)
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
