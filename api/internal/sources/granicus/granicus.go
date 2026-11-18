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
	dayStart  time.Time
	clipTime  bool
	link      string
	agenda    bool
	media     bool
}

func (e entry) rank(o entry) int {
	return cmp.Or(cmp.Compare(e.clipID, o.clipID), cmp.Compare(e.eventID, o.eventID))
}

type category string

const (
	agendaClips  category = "agenda feed clips"
	agendaEvents category = "agenda feed events"
	videoClips   category = "video feed clips"
)

type tally map[category]*struct{ good, bad int }

func (t tally) add(c category, good bool) {
	if t[c] == nil {
		t[c] = &struct{ good, bad int }{}
	}
	if good {
		t[c].good++
	} else {
		t[c].bad++
	}
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
	now := a.clock.Now()
	inWindow := tally{}
	entries := a.entries(ctx, agendas, true, window, now, inWindow)
	entries = append(entries, a.entries(ctx, videos, false, window, now, inWindow)...)
	for _, c := range []category{agendaClips, agendaEvents, videoClips} {
		if n := inWindow[c]; n != nil && n.good == 0 {
			return nil, fmt.Errorf("%w: %s: all %d board items among the %s in the window are unreadable", ErrMalformed, a.cfg.Host, n.bad, c)
		}
	}

	groups := map[string][]entry{}
	for _, e := range entries {
		groups[e.key] = append(groups[e.key], e)
	}
	var meetings []sources.Meeting
	for key, group := range groups {
		m := a.meeting(ctx, key, group, now)
		if window.Contains(m.StartsAt) {
			meetings = append(meetings, m)
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

func (a *Adapter) entries(ctx context.Context, items []item, agendaFeed bool, window sources.Window, now time.Time, inWindow tally) []entry {
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
		if !agendaFeed && err == nil && e.clipID == 0 {
			continue
		}
		inside := e.startsAt.IsZero() || window.Contains(e.startsAt)
		if inside {
			inWindow.add(categorize(e, agendaFeed, now), err == nil)
		}
		switch {
		case err != nil && inside:
			a.logger.WarnContext(ctx, "granicus: skipped an unreadable board item", "host", a.cfg.Host, "title", it.Title, "error", err)
		case err != nil:
			a.logger.DebugContext(ctx, "granicus: skipped an unreadable board item outside the window", "host", a.cfg.Host, "title", it.Title, "error", err)
		default:
			out = append(out, e)
		}
	}
	return out
}

func categorize(e entry, agendaFeed bool, now time.Time) category {
	switch {
	case !agendaFeed:
		return videoClips
	case e.clipID > 0 || strings.Contains(e.link, "clip_id="):
		return agendaClips
	case e.eventID > 0 || strings.Contains(e.link, "event_id="):
		return agendaEvents
	case !e.startsAt.IsZero() && e.startsAt.Before(now):
		return agendaClips
	default:
		return agendaEvents
	}
}

func (a *Adapter) entry(it item, t title, name string, agendaFeed bool) (entry, error) {
	e := entry{name: name, cancelled: t.cancelled, link: strings.TrimSpace(it.Link), agenda: agendaFeed}
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
	e.dayStart = time.Date(day.Year(), day.Month(), day.Day(), a.startHour, a.startMin, 0, 0, a.loc).UTC()
	e.startsAt = e.dayStart
	if clipID > 0 && partsOK && sameDay(parts, day) {
		e.startsAt, e.clipTime = parts.UTC(), true
	}
	if idErr != nil {
		return e, idErr
	}
	e.key = slug(name) + "/" + day.Format(time.DateOnly)
	e.media = !agendaFeed && it.Enclosure != nil && strings.TrimSpace(it.Enclosure.URL) != ""
	return e, nil
}

func sameDay(x, y time.Time) bool {
	return x.Year() == y.Year() && x.Month() == y.Month() && x.Day() == y.Day()
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

func (a *Adapter) meeting(ctx context.Context, key string, group []entry, now time.Time) sources.Meeting {
	slices.SortStableFunc(group, entry.rank)
	first := group[0]
	startsAt := first.dayStart
	var agenda *entry
	var recordingURL string
	clipStarts := map[int]time.Time{}
	timed := false
	for i := range group {
		e := group[i]
		if e.agenda {
			agenda = &group[i]
		}
		if e.media && recordingURL == "" {
			recordingURL = e.link
		}
		if e.clipTime {
			clipStarts[e.clipID] = e.startsAt
			if !timed {
				startsAt, timed = e.startsAt, true
			}
		}
	}
	if distinct(clipStarts) {
		a.logger.WarnContext(ctx, "granicus: clips of one board meeting have different start times", "host", a.cfg.Host, "source_id", key, "clip_starts", fmt.Sprint(clipStarts))
	}
	cancelled := group[len(group)-1].cancelled
	agendaURL := ""
	if agenda != nil {
		cancelled = agenda.cancelled
		agendaURL = agenda.link
	}
	if cancelled {
		recordingURL = ""
	}
	status := sources.StatusScheduled
	switch {
	case cancelled:
		status = sources.StatusCancelled
	case recordingURL != "" && !startsAt.After(now):
		status = sources.StatusHeld
	}
	return sources.Meeting{
		SourceID:     key,
		Title:        first.name,
		StartsAt:     startsAt,
		Status:       status,
		PageURL:      cmp.Or(recordingURL, agendaURL, first.link),
		AgendaURL:    agendaURL,
		RecordingURL: recordingURL,
	}
}

func distinct(starts map[int]time.Time) bool {
	var seen time.Time
	for _, s := range starts {
		if !seen.IsZero() && !s.Equal(seen) {
			return true
		}
		seen = s
	}
	return false
}
