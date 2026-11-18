package httpapi

import (
	"context"
	"sync"
	"time"
)

const (
	feedCacheTTL     = time.Minute
	feedCacheMaxSize = 256
	feedLoadTimeout  = 10 * time.Second
)

type renderedFeed struct {
	body []byte
	etag string
}

type feedEntry struct {
	feed    renderedFeed
	expires time.Time
}

type feedCall struct {
	done chan struct{}
	feed renderedFeed
	err  error
}

type feedLoader func(ctx context.Context, slug string) (renderedFeed, error)

type feedCache struct {
	ttl      time.Duration
	maxSize  int
	now      func() time.Time
	mu       sync.Mutex
	entries  map[string]feedEntry
	inflight map[string]*feedCall
}

func newFeedCache() *feedCache {
	return &feedCache{
		ttl:      feedCacheTTL,
		maxSize:  feedCacheMaxSize,
		now:      time.Now,
		entries:  map[string]feedEntry{},
		inflight: map[string]*feedCall{},
	}
}

func (c *feedCache) get(ctx context.Context, slug string, load feedLoader) (renderedFeed, error) {
	c.mu.Lock()
	if e, ok := c.entries[slug]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.feed, nil
	}
	call, running := c.inflight[slug]
	if !running {
		call = &feedCall{done: make(chan struct{})}
		c.inflight[slug] = call
		go c.load(context.WithoutCancel(ctx), slug, call, load)
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.feed, call.err
	case <-ctx.Done():
		return renderedFeed{}, ctx.Err()
	}
}

func (c *feedCache) load(ctx context.Context, slug string, call *feedCall, load feedLoader) {
	ctx, cancel := context.WithTimeout(ctx, feedLoadTimeout)
	defer cancel()
	call.feed, call.err = load(ctx, slug)

	c.mu.Lock()
	delete(c.inflight, slug)
	if call.err == nil {
		c.put(slug, call.feed)
	}
	c.mu.Unlock()
	close(call.done)
}

func (c *feedCache) put(slug string, feed renderedFeed) {
	now := c.now()
	if _, ok := c.entries[slug]; !ok && len(c.entries) >= c.maxSize {
		oldest := ""
		for key, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, key)
				continue
			}
			if oldest == "" || e.expires.Before(c.entries[oldest].expires) {
				oldest = key
			}
		}
		if len(c.entries) >= c.maxSize {
			delete(c.entries, oldest)
		}
	}
	c.entries[slug] = feedEntry{feed: feed, expires: now.Add(c.ttl)}
}
