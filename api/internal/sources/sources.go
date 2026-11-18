package sources

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

type Status string

const (
	StatusScheduled Status = "scheduled"
	StatusHeld      Status = "held"
	StatusCancelled Status = "cancelled"
)

type Meeting struct {
	SourceID     string
	Title        string
	StartsAt     time.Time
	Status       Status
	PageURL      string
	AgendaURL    string
	RecordingURL string
}

type Window struct {
	From time.Time
	To   time.Time
}

func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.From) && !t.After(w.To)
}

type Adapter interface {
	Meetings(ctx context.Context, window Window) ([]Meeting, error)
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type Deps struct {
	Fetcher fetch.Fetcher
	Clock   Clock
}

type Constructor func(Deps) Adapter

type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry(deps Deps, constructors map[string]Constructor) *Registry {
	adapters := make(map[string]Adapter, len(constructors))
	for slug, construct := range constructors {
		adapters[slug] = construct(deps)
	}
	return &Registry{adapters: adapters}
}

func (r *Registry) Lookup(slug string) (Adapter, bool) {
	if r == nil {
		return nil, false
	}
	a, ok := r.adapters[slug]
	return a, ok
}

func (r *Registry) Slugs() []string {
	if r == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(r.adapters))
}

var ErrInvalidMeeting = errors.New("sources: invalid meeting")

func Validate(meetings []Meeting) error {
	seen := make(map[string]bool, len(meetings))
	for i, m := range meetings {
		if err := m.validate(); err != nil {
			return fmt.Errorf("%w: meeting %d (%q): %w", ErrInvalidMeeting, i, m.SourceID, err)
		}
		if seen[m.SourceID] {
			return fmt.Errorf("%w: duplicate source id %q", ErrInvalidMeeting, m.SourceID)
		}
		seen[m.SourceID] = true
	}
	return nil
}

func (m Meeting) validate() error {
	switch {
	case m.SourceID == "":
		return errors.New("empty source id")
	case strings.TrimSpace(m.Title) == "":
		return errors.New("empty title")
	case m.StartsAt.IsZero():
		return errors.New("zero start time")
	case m.StartsAt.Location() != time.UTC:
		return fmt.Errorf("start time %s is not in UTC", m.StartsAt)
	case m.Status != StatusScheduled && m.Status != StatusHeld && m.Status != StatusCancelled:
		return fmt.Errorf("unknown status %q", m.Status)
	case m.PageURL == "":
		return errors.New("empty page url")
	}
	for _, u := range []string{m.PageURL, m.AgendaURL, m.RecordingURL} {
		if u != "" && !absoluteHTTP(u) {
			return fmt.Errorf("url %q is not an absolute http or https url", u)
		}
	}
	return nil
}

func absoluteHTTP(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func Retryable(err error) bool {
	return fetch.IsRetryable(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
