package vctc

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
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
	ErrInvalidConfig = errors.New("vctc: invalid config")
	ErrMalformed     = errors.New("vctc: malformed response")
)

const defaultPageSize = 20

type Config struct {
	Host      string
	Boards    []Board
	TimeZone  string
	StartTime string
	PageSize  int
	YouTube   YouTube
}

type Board struct {
	TypeID int
	Name   string
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
	a.logger = deps.Log().With("host", a.cfg.Host)
	return &a
}

func (c Config) adapter() (Adapter, error) {
	c.Boards = slices.Clone(c.Boards)
	c.PageSize = cmp.Or(c.PageSize, defaultPageSize)
	switch {
	case c.Host == "" || strings.ContainsAny(c.Host, "/?#:@ "):
		return Adapter{}, fmt.Errorf("%w: host %q", ErrInvalidConfig, c.Host)
	case len(c.Boards) == 0:
		return Adapter{}, fmt.Errorf("%w: no boards", ErrInvalidConfig)
	case slices.ContainsFunc(c.Boards, func(b Board) bool { return b.TypeID <= 0 || normalize(b.Name) == "" }):
		return Adapter{}, fmt.Errorf("%w: boards %+v", ErrInvalidConfig, c.Boards)
	case c.PageSize < 1 || c.PageSize > 100:
		return Adapter{}, fmt.Errorf("%w: page size %d", ErrInvalidConfig, c.PageSize)
	case c.TimeZone == "":
		return Adapter{}, fmt.Errorf("%w: no time zone", ErrInvalidConfig)
	}
	if err := c.YouTube.validate(); err != nil {
		return Adapter{}, err
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

func (a *Adapter) typeIDs() []int {
	ids := make([]int, 0, len(a.cfg.Boards))
	for _, b := range a.cfg.Boards {
		ids = append(ids, b.TypeID)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func (a *Adapter) postsURL() string {
	ids := a.typeIDs()
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	q := url.Values{}
	q.Set("meeting_type", strings.Join(parts, ","))
	q.Set("per_page", strconv.Itoa(a.cfg.PageSize))
	q.Set("orderby", "date")
	q.Set("order", "desc")
	q.Set("_fields", "id,link,title,content,meta,meeting_type")
	return "https://" + a.cfg.Host + "/wp-json/wp/v2/meeting?" + q.Encode()
}

type post struct {
	ID    int    `json:"id"`
	Link  string `json:"link"`
	Title struct {
		Rendered string `json:"rendered"`
	} `json:"title"`
	Content struct {
		Rendered string `json:"rendered"`
	} `json:"content"`
	Meta  json.RawMessage `json:"meta"`
	Types []int           `json:"meeting_type"`
}

type meta struct {
	Date string `json:"_meeting_date"`
	Time string `json:"_meeting_time"`
}

type entry struct {
	sourceID     string
	board        string
	title        string
	startsAt     time.Time
	cancelled    bool
	pageURL      string
	agendaURL    string
	recordingURL string
}

type windowDays struct {
	first time.Time
	last  time.Time
}

func (a *Adapter) days(window sources.Window) windowDays {
	day := func(t time.Time) time.Time {
		l := t.In(a.loc)
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	}
	return windowDays{first: day(window.From), last: day(window.To)}
}

func (a *Adapter) Meetings(ctx context.Context, window sources.Window) ([]sources.Meeting, error) {
	entries, err := a.entries(ctx, window)
	if err != nil {
		return nil, err
	}
	now := a.clock.Now()
	if a.cfg.YouTube.enabled() {
		videos, err := a.videos(ctx, window)
		if err != nil {
			return nil, err
		}
		a.matchRecordings(ctx, entries, videos, now)
	}
	a.warnDuplicates(ctx, entries)

	var meetings []sources.Meeting
	for _, e := range entries {
		if !window.Contains(e.startsAt) {
			continue
		}
		status := sources.StatusScheduled
		switch {
		case e.cancelled:
			status = sources.StatusCancelled
		case e.recordingURL != "" && !e.startsAt.After(now):
			status = sources.StatusHeld
		}
		meetings = append(meetings, sources.Meeting{
			SourceID:     e.sourceID,
			Title:        e.title,
			StartsAt:     e.startsAt,
			Status:       status,
			PageURL:      e.pageURL,
			AgendaURL:    e.agendaURL,
			RecordingURL: e.recordingURL,
		})
	}
	slices.SortFunc(meetings, func(x, y sources.Meeting) int {
		return cmp.Or(x.StartsAt.Compare(y.StartsAt), strings.Compare(x.SourceID, y.SourceID))
	})
	return meetings, nil
}

func (a *Adapter) entries(ctx context.Context, window sources.Window) ([]entry, error) {
	resp, err := a.fetcher.Fetch(ctx, fetch.Request{URL: a.postsURL()})
	if err != nil {
		return nil, fmt.Errorf("vctc %s meetings: %w", a.cfg.Host, err)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(resp.Body, &raws); err != nil {
		return nil, fmt.Errorf("%w: %s meetings: %w", ErrMalformed, a.cfg.Host, err)
	}
	days := a.days(window)
	var out []entry
	decoded, good, bad := 0, 0, 0
	for i, raw := range raws {
		var p post
		if err := json.Unmarshal(raw, &p); err != nil {
			a.logger.WarnContext(ctx, "vctc: skipped a meeting post that does not decode", "index", i, "error", err)
			continue
		}
		decoded++
		t := parseTitle(p.Title.Rendered)
		board, ok := a.board(p.Types, t)
		if !ok {
			a.logDropped(ctx, p, t)
			continue
		}
		e, err := a.entry(ctx, p, t, board)
		inside := (e.startsAt.IsZero() && t.overlaps(days)) || (!e.startsAt.IsZero() && window.Contains(e.startsAt))
		switch {
		case err != nil && inside:
			bad++
			a.logger.WarnContext(ctx, "vctc: skipped an unreadable board meeting post", "post", p.ID, "title", t.full, "error", err)
		case err != nil:
			a.logger.DebugContext(ctx, "vctc: skipped an unreadable board meeting post outside the window", "post", p.ID, "title", t.full, "error", err)
		default:
			if inside {
				good++
			}
			out = append(out, e)
		}
	}
	if len(raws) > 0 && decoded == 0 {
		return nil, fmt.Errorf("%w: %s meetings: none of the %d posts decodes", ErrMalformed, a.cfg.Host, len(raws))
	}
	if good == 0 && bad > 0 {
		return nil, fmt.Errorf("%w: %s meetings: all %d board meeting posts in the window are unreadable", ErrMalformed, a.cfg.Host, bad)
	}
	return out, nil
}

func (a *Adapter) board(types []int, t title) (string, bool) {
	if len(types) == 0 {
		return "", false
	}
	known := a.typeIDs()
	for _, id := range types {
		if !slices.Contains(known, id) {
			return "", false
		}
	}
	for _, b := range a.cfg.Boards {
		if !slices.Contains(types, b.TypeID) {
			continue
		}
		name := normalize(b.Name)
		if strings.EqualFold(name, t.name) || strings.EqualFold(name, t.whole) {
			return name, true
		}
	}
	return "", false
}

func (a *Adapter) logDropped(ctx context.Context, p post, t title) {
	known := a.typeIDs()
	tagged := len(p.Types) > 0 && !slices.ContainsFunc(p.Types, func(id int) bool { return !slices.Contains(known, id) })
	lower := strings.ToLower(t.full)
	looksLikeBoard := strings.Contains(lower, "commission") || strings.Contains(lower, "board") || strings.Contains(lower, "special meeting")
	switch {
	case tagged && looksLikeBoard:
		a.logger.WarnContext(ctx, "vctc: dropped a post of a board meeting type whose title is not a configured board meeting", "post", p.ID, "title", t.full, "types", fmt.Sprint(p.Types))
	case tagged || looksLikeBoard:
		a.logger.InfoContext(ctx, "vctc: dropped a post that is not a configured board meeting", "post", p.ID, "title", t.full, "types", fmt.Sprint(p.Types))
	}
}

func (a *Adapter) entry(ctx context.Context, p post, t title, board string) (entry, error) {
	e := entry{sourceID: strconv.Itoa(p.ID), board: board, title: t.full, cancelled: t.cancelled, pageURL: strings.TrimSpace(p.Link)}
	m, err := decodeMeta(p.Meta)
	if err != nil {
		return e, err
	}
	day, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(m.Date), a.loc)
	if err != nil {
		return e, fmt.Errorf("meeting date %q", m.Date)
	}
	e.startsAt = a.start(ctx, p, day, m.Time)
	switch {
	case p.ID <= 0:
		return e, fmt.Errorf("post id %d", p.ID)
	case !absolute(e.pageURL):
		return e, fmt.Errorf("link %q is not an absolute url", p.Link)
	}
	if !t.agrees(day) {
		a.logger.WarnContext(ctx, "vctc: the post title names another date than the meeting date; using the meeting date", "post", p.ID, "title", t.full, "meeting_date", m.Date)
	}
	e.agendaURL = a.agenda(ctx, p, e.pageURL)
	return e, nil
}

func decodeMeta(raw json.RawMessage) (meta, error) {
	var m meta
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("[]")) {
		return m, errors.New("no meta")
	}
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return m, fmt.Errorf("meta: %w", err)
	}
	return m, nil
}

var timeLayouts = []string{"15:04", "15:04:05", "3:04 PM", "3:04PM", "3:04 pm", "3:04pm"}

func (a *Adapter) start(ctx context.Context, p post, day time.Time, raw string) time.Time {
	hour, minute := a.startHour, a.startMin
	text := normalize(raw)
	switch parsed, ok := parseClock(text); {
	case text == "":
		a.logger.InfoContext(ctx, "vctc: board meeting post has no start time; using the configured start time", "post", p.ID, "start_time", a.cfg.StartTime)
	case !ok:
		a.logger.WarnContext(ctx, "vctc: board meeting post has an unreadable start time; using the configured start time", "post", p.ID, "time", raw, "start_time", a.cfg.StartTime)
	default:
		hour, minute = parsed.Hour(), parsed.Minute()
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, a.loc).UTC()
}

func parseClock(text string) (time.Time, bool) {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, text); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (a *Adapter) agenda(ctx context.Context, p post, pageURL string) string {
	links := agendaLinks(p.Content.Rendered)
	if len(links) == 0 {
		return ""
	}
	chosen := links[0]
	if len(links) > 1 {
		for _, l := range links {
			lower := strings.ToLower(l.text + " " + l.href)
			if strings.Contains(lower, "amended") || strings.Contains(lower, "revised") {
				chosen = l
				break
			}
		}
		a.logger.InfoContext(ctx, "vctc: several links under the current agenda heading", "post", p.ID, "links", len(links), "chosen", chosen.href)
	}
	base, _ := url.Parse(pageURL)
	ref, err := url.Parse(chosen.href)
	if err != nil {
		a.logger.WarnContext(ctx, "vctc: unreadable agenda link", "post", p.ID, "href", chosen.href, "error", err)
		return ""
	}
	resolved := base.ResolveReference(ref).String()
	if !absolute(resolved) {
		a.logger.WarnContext(ctx, "vctc: agenda link is not an http url", "post", p.ID, "href", chosen.href)
		return ""
	}
	return resolved
}

func absolute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (a *Adapter) warnDuplicates(ctx context.Context, entries []entry) {
	seen := map[string]string{}
	for _, e := range entries {
		key := e.board + "/" + e.startsAt.In(a.loc).Format(time.DateOnly)
		if other, ok := seen[key]; ok {
			a.logger.WarnContext(ctx, "vctc: two posts for one board on one day; both are stored", "board", e.board, "date", key, "posts", other+","+e.sourceID)
			continue
		}
		seen[key] = e.sourceID
	}
}
