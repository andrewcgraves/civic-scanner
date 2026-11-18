package featureflags

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	staleGraceMultiple = 5
	defaultMaxEntries  = 1024
)

var errRefreshFailed = errors.New("flag refresh failed")

type flagValues map[string]any

type fetchFunc func(distinctID string) (flagValues, error)

type flagCache struct {
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	fetch      fetchFunc
	logger     *slog.Logger

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	values    flagValues
	fetchedAt time.Time
	checkedAt time.Time
	inflight  *refresh
}

type refresh struct {
	done   chan struct{}
	values flagValues
	err    error
}

func newFlagCache(ttl time.Duration, fetch fetchFunc, logger *slog.Logger) *flagCache {
	return &flagCache{
		ttl:        ttl,
		maxEntries: defaultMaxEntries,
		now:        time.Now,
		fetch:      fetch,
		logger:     logger,
		entries:    make(map[string]*cacheEntry),
	}
}

func (c *flagCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *flagCache) values(ctx context.Context, distinctID string) (flagValues, error) {
	if c.ttl <= 0 {
		return c.fetchWithContext(ctx, distinctID)
	}
	c.mu.Lock()
	entry := c.entries[distinctID]
	if entry != nil && entry.inflight == nil && c.now().Sub(entry.checkedAt) < c.ttl {
		values, err := c.servableLocked(entry)
		c.mu.Unlock()
		return values, err
	}
	var call *refresh
	if entry != nil {
		call = entry.inflight
	}
	if call == nil {
		var ok bool
		call, ok = c.startRefreshLocked(distinctID)
		if !ok {
			c.mu.Unlock()
			return c.fetchWithContext(ctx, distinctID)
		}
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.values, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *flagCache) startRefreshLocked(distinctID string) (*refresh, bool) {
	entry := c.entries[distinctID]
	if entry == nil {
		if len(c.entries) >= c.maxEntries && !c.evictLocked() {
			return nil, false
		}
		entry = &cacheEntry{}
		c.entries[distinctID] = entry
	}
	call := &refresh{done: make(chan struct{})}
	entry.inflight = call
	go c.run(distinctID, entry, call)
	return call, true
}

func (c *flagCache) run(distinctID string, entry *cacheEntry, call *refresh) {
	values, err := c.fetch(distinctID)

	c.mu.Lock()
	now := c.now()
	entry.checkedAt = now
	entry.inflight = nil
	if err == nil {
		entry.values = values
		entry.fetchedAt = now
	}
	call.values, call.err = c.servableLocked(entry)
	c.mu.Unlock()

	if err != nil {
		c.logger.Warn("feature flag refresh failed", "error", err, "serving_stale", call.err == nil && call.values != nil)
		if call.err != nil {
			call.err = fmt.Errorf("%w: %w", errRefreshFailed, err)
		}
	}
	close(call.done)
}

func (c *flagCache) servableLocked(entry *cacheEntry) (flagValues, error) {
	if entry.values != nil && c.now().Sub(entry.fetchedAt) <= staleGraceMultiple*c.ttl {
		return entry.values, nil
	}
	return nil, errRefreshFailed
}

func (c *flagCache) evictLocked() bool {
	var oldestID string
	var oldest time.Time
	found := false
	for id, entry := range c.entries {
		if entry.inflight != nil {
			continue
		}
		if !found || entry.checkedAt.Before(oldest) {
			oldestID, oldest, found = id, entry.checkedAt, true
		}
	}
	if found {
		delete(c.entries, oldestID)
	}
	return found
}

func (c *flagCache) fetchWithContext(ctx context.Context, distinctID string) (flagValues, error) {
	type result struct {
		values flagValues
		err    error
	}
	done := make(chan result, 1)
	go func() {
		values, err := c.fetch(distinctID)
		done <- result{values, err}
	}()
	select {
	case r := <-done:
		return r.values, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
