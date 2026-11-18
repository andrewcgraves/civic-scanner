package sourcestest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
)

type Adapter struct {
	mu       sync.Mutex
	meetings []sources.Meeting
	err      error
	calls    []sources.Window
}

func Returning(meetings ...sources.Meeting) *Adapter {
	return &Adapter{meetings: slices.Clone(meetings)}
}

func Failing(err error) *Adapter {
	return &Adapter{err: err}
}

func (a *Adapter) Set(meetings ...sources.Meeting) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.meetings = slices.Clone(meetings)
	a.err = nil
}

func (a *Adapter) Fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func (a *Adapter) Meetings(ctx context.Context, window sources.Window) ([]sources.Meeting, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, window)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.err != nil {
		return nil, a.err
	}
	return slices.Clone(a.meetings), nil
}

func (a *Adapter) Calls() []sources.Window {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

func Constructor(a sources.Adapter) sources.Constructor {
	return func(sources.Deps) sources.Adapter { return a }
}

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(now time.Time) *Clock {
	return &Clock{now: now.UTC()}
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now.UTC()
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
