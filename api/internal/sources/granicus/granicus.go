package granicus

import (
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
)

var (
	ErrInvalidConfig = errors.New("granicus: invalid config")
	ErrMalformed     = errors.New("granicus: malformed response")
)

type Config struct {
	Host       string
	ViewID     int
	BoardNames []string
	TimeZone   string
	StartTime  string
}

type Adapter struct {
	cfg       Config
	loc       *time.Location
	startHour int
	startMin  int
	fetcher   fetch.Fetcher
	clock     sources.Clock
	logger    *slog.Logger
}

func New(cfg Config, deps sources.Deps) (*Adapter, error) {
	a, err := cfg.adapter()
	if err != nil {
		return nil, err
	}
	return a.with(deps), nil
}

func Constructor(cfg Config) sources.Constructor {
	a, err := cfg.adapter()
	if err != nil {
		panic(err)
	}
	return func(deps sources.Deps) sources.Adapter {
		return a.with(deps)
	}
}

func (a Adapter) with(deps sources.Deps) *Adapter {
	a.fetcher = deps.Fetcher
	a.clock = deps.Clock
	a.logger = deps.Log()
	return &a
}

func (c Config) adapter() (Adapter, error) {
	c.BoardNames = slices.Clone(c.BoardNames)
	switch {
	case c.Host == "" || strings.ContainsAny(c.Host, "/?#:@ "):
		return Adapter{}, fmt.Errorf("%w: host %q", ErrInvalidConfig, c.Host)
	case c.ViewID <= 0:
		return Adapter{}, fmt.Errorf("%w: view id %d", ErrInvalidConfig, c.ViewID)
	case len(c.BoardNames) == 0:
		return Adapter{}, fmt.Errorf("%w: no board names", ErrInvalidConfig)
	case slices.ContainsFunc(c.BoardNames, func(n string) bool { return slug(n) == "" }):
		return Adapter{}, fmt.Errorf("%w: board names %q", ErrInvalidConfig, c.BoardNames)
	case c.TimeZone == "":
		return Adapter{}, fmt.Errorf("%w: no time zone", ErrInvalidConfig)
	}
	start, err := time.Parse("15:04", c.StartTime)
	if err != nil {
		return Adapter{}, fmt.Errorf("%w: start time %q", ErrInvalidConfig, c.StartTime)
	}
	loc, err := time.LoadLocation(c.TimeZone)
	if err != nil {
		return Adapter{}, fmt.Errorf("%w: time zone: %w", ErrInvalidConfig, err)
	}
	return Adapter{cfg: c, loc: loc, startHour: start.Hour(), startMin: start.Minute()}, nil
}

type feed struct {
	XMLName xml.Name `xml:"rss"`
	Channel *struct {
		Items []item `xml:"item"`
	} `xml:"channel"`
}

type item struct {
	Title     string     `xml:"title"`
	Link      string     `xml:"link"`
	DateParts *dateParts `xml:"pubDateParts"`
	Enclosure *struct {
		URL string `xml:"url,attr"`
	} `xml:"enclosure"`
}

type dateParts struct {
	Year   string `xml:"yr,attr"`
	Month  string `xml:"mo,attr"`
	Day    string `xml:"day,attr"`
	Hour   string `xml:"hr,attr"`
	Minute string `xml:"min,attr"`
}

type entry struct {
	key       string
	name      string
	cancelled bool
	clipID    int
	eventID   int
	startsAt  time.Time
	link      string
	agenda    bool
	media     bool
}

type tally struct {
	good, bad int
}

func (a *Adapter) Meetings(ctx context.Context, window sources.Window) ([]sources.Meeting, error) {
	agendas, err := a.items(ctx, "agendas")
	if err != nil {
		return nil, err
	}
	videos, err := a.items(ctx, "vod")
	if err != nil {
		return nil, err
	}
	var inWindow tally
	entries := a.entries(ctx, agendas, true, window, &inWindow)
	entries = append(entries, a.entries(ctx, videos, false, window, &inWindow)...)
	if inWindow.bad > 0 && inWindow.good == 0 {
		return nil, fmt.Errorf("%w: %s: all %d board items in the window are unreadable", ErrMalformed, a.cfg.Host, inWindow.bad)
	}

	now := a.clock.Now()
	var meetings []sources.Meeting
	for _, m := range group(entries) {
		meeting := m.meeting(now)
		if window.Contains(meeting.StartsAt) {
			meetings = append(meetings, meeting)
		}
	}
	slices.SortFunc(meetings, func(x, y sources.Meeting) int {
		return cmp.Or(x.StartsAt.Compare(y.StartsAt), strings.Compare(x.SourceID, y.SourceID))
	})
	return meetings, nil
}

func (a *Adapter) items(ctx context.Context, mode string) ([]item, error) {
	target := fmt.Sprintf("https://%s/ViewPublisherRSS.php?view_id=%d&mode=%s", a.cfg.Host, a.cfg.ViewID, mode)
	resp, err := a.fetcher.Fetch(ctx, fetch.Request{URL: target})
	if err != nil {
		return nil, fmt.Errorf("granicus %s %s feed: %w", a.cfg.Host, mode, err)
	}
	var f feed
	if err := xml.Unmarshal(resp.Body, &f); err != nil {
		return nil, fmt.Errorf("%w: %s %s feed: %w", ErrMalformed, a.cfg.Host, mode, err)
	}
	if f.Channel == nil {
		return nil, fmt.Errorf("%w: %s %s feed has no channel", ErrMalformed, a.cfg.Host, mode)
	}
	return f.Channel.Items, nil
}

func (a *Adapter) entries(ctx context.Context, items []item, agendaFeed bool, window sources.Window, inWindow *tally) []entry {
	var out []entry
	for _, it := range items {
		t := parseTitle(it.Title)
		name, ok := a.boardName(t.name)
		if !ok {
			if strings.Contains(strings.ToLower(it.Title), "board") {
				a.logger.InfoContext(ctx, "granicus: dropped an item that is not a configured board meeting", "host", a.cfg.Host, "title", it.Title)
			}
			continue
		}
		e, err := a.entry(it, t, name, agendaFeed)
		inside := e.startsAt.IsZero() || window.Contains(e.startsAt)
		switch {
		case err != nil && inside:
			inWindow.bad++
			a.logger.WarnContext(ctx, "granicus: skipped an unreadable board item", "host", a.cfg.Host, "title", it.Title, "error", err)
		case err != nil:
			a.logger.DebugContext(ctx, "granicus: skipped an unreadable board item outside the window", "host", a.cfg.Host, "title", it.Title, "error", err)
		default:
			if inside {
				inWindow.good++
			}
			if agendaFeed || e.clipID > 0 {
				out = append(out, e)
			}
		}
	}
	return out
}

func (a *Adapter) entry(it item, t title, name string, agendaFeed bool) (entry, error) {
	e := entry{name: name, cancelled: t.cancelled, link: strings.TrimSpace(it.Link)}
	clipID, eventID, idErr := ids(e.link)
	e.clipID, e.eventID = clipID, eventID
	parts, partsOK := a.parts(it.DateParts)
	day := t.date
	switch {
	case t.hasDate:
	case clipID > 0 && partsOK:
		day = parts
	default:
		return e, errors.Join(idErr, errors.New("no meeting date in the title"))
	}
	e.startsAt = time.Date(day.Year(), day.Month(), day.Day(), a.startHour, a.startMin, 0, 0, a.loc).UTC()
	if clipID > 0 && partsOK {
		e.startsAt = parts.UTC()
	}
	if idErr != nil {
		return e, idErr
	}
	e.key = slug(name) + "/" + day.Format(time.DateOnly)
	e.agenda = agendaFeed
	e.media = !agendaFeed && it.Enclosure != nil && strings.TrimSpace(it.Enclosure.URL) != ""
	return e, nil
}

func ids(link string) (clipID, eventID int, err error) {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return 0, 0, fmt.Errorf("link %q is not an absolute url", link)
	}
	q := u.Query()
	if id, err := strconv.Atoi(q.Get("clip_id")); err == nil && id > 0 {
		return id, 0, nil
	}
	if id, err := strconv.Atoi(q.Get("event_id")); err == nil && id > 0 {
		return 0, id, nil
	}
	return 0, 0, fmt.Errorf("link %q has neither a clip id nor an event id", link)
}

func (a *Adapter) parts(p *dateParts) (time.Time, bool) {
	if p == nil {
		return time.Time{}, false
	}
	var n [5]int
	for i, raw := range []string{p.Year, p.Month, p.Day, p.Hour, p.Minute} {
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return time.Time{}, false
		}
		n[i] = v
	}
	year, month, day, hour, minute := n[0], n[1], n[2], n[3], n[4]
	if month < 1 || month > 12 || day < 1 || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return time.Time{}, false
	}
	local := time.Date(year, time.Month(month), day, hour, minute, 0, 0, a.loc)
	if local.Day() != day {
		return time.Time{}, false
	}
	return local, true
}

func (a *Adapter) boardName(name string) (string, bool) {
	for _, n := range a.cfg.BoardNames {
		if strings.EqualFold(normalize(n), name) {
			return normalize(n), true
		}
	}
	return "", false
}

type meeting struct {
	sourceID string
	entries  []entry
}

func group(entries []entry) []meeting {
	byKey := map[string][]entry{}
	var keys []string
	for _, e := range entries {
		if _, ok := byKey[e.key]; !ok {
			keys = append(keys, e.key)
		}
		byKey[e.key] = append(byKey[e.key], e)
	}
	var out []meeting
	for _, key := range keys {
		for i, entries := range sessions(byKey[key]) {
			id := key
			if i > 0 {
				id = key + "/" + strconv.Itoa(i+1)
			}
			out = append(out, meeting{sourceID: id, entries: entries})
		}
	}
	return out
}

func sessions(entries []entry) [][]entry {
	var clips, events []entry
	for _, e := range entries {
		if e.clipID > 0 {
			clips = append(clips, e)
		} else {
			events = append(events, e)
		}
	}
	if len(clips) == 0 {
		slices.SortFunc(events, func(x, y entry) int { return cmp.Compare(x.eventID, y.eventID) })
		var out [][]entry
		for _, e := range events {
			if len(out) == 0 || out[len(out)-1][0].eventID != e.eventID {
				out = append(out, []entry{e})
			}
		}
		return out
	}
	slices.SortFunc(clips, func(x, y entry) int {
		return cmp.Or(x.startsAt.Compare(y.startsAt), cmp.Compare(x.clipID, y.clipID))
	})
	var out [][]entry
	for _, c := range clips {
		if n := len(out); n > 0 && out[n-1][0].startsAt.Equal(c.startsAt) {
			out[n-1] = append(out[n-1], c)
			continue
		}
		out = append(out, []entry{c})
	}
	for _, s := range out {
		slices.SortStableFunc(s, func(x, y entry) int { return cmp.Compare(x.clipID, y.clipID) })
	}
	return out
}

func (m meeting) meeting(now time.Time) sources.Meeting {
	first := m.entries[0]
	var agendaURL, recordingURL, anyLink string
	cancelled := false
	for _, e := range m.entries {
		if anyLink == "" {
			anyLink = e.link
		}
		if e.agenda {
			agendaURL = e.link
			cancelled = e.cancelled
		}
		if e.media && recordingURL == "" {
			recordingURL = e.link
		}
	}
	if agendaURL == "" {
		cancelled = m.entries[len(m.entries)-1].cancelled
	}
	if cancelled {
		recordingURL = ""
	}
	pageURL := cmp.Or(recordingURL, agendaURL, anyLink)
	status := sources.StatusScheduled
	switch {
	case cancelled:
		status = sources.StatusCancelled
	case recordingURL != "" && !first.startsAt.After(now):
		status = sources.StatusHeld
	}
	return sources.Meeting{
		SourceID:     m.sourceID,
		Title:        first.name,
		StartsAt:     first.startsAt,
		Status:       status,
		PageURL:      pageURL,
		AgendaURL:    agendaURL,
		RecordingURL: recordingURL,
	}
}
