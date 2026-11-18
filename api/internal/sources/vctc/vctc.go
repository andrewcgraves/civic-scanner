package vctc

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
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

var committeeWord = regexp.MustCompile(`^[A-Za-z0-9]+$`)

type Config struct {
	Host           string
	Boards         []Board
	TimeZone       string
	StartTime      string
	PageSize       int
	CommitteeWords []string
	YouTube        YouTube
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
	c.CommitteeWords = slices.Clone(c.CommitteeWords)
	c.PageSize = cmp.Or(c.PageSize, defaultPageSize)
	switch {
	case c.Host == "" || strings.ContainsAny(c.Host, "/?#:@ "):
		return Adapter{}, fmt.Errorf("%w: host %q", ErrInvalidConfig, c.Host)
	case len(c.Boards) == 0:
		return Adapter{}, fmt.Errorf("%w: no boards", ErrInvalidConfig)
	case slices.ContainsFunc(c.Boards, func(b Board) bool { return b.TypeID <= 0 || normalize(b.Name) == "" }):
		return Adapter{}, fmt.Errorf("%w: boards %+v", ErrInvalidConfig, c.Boards)
	case slices.ContainsFunc(c.CommitteeWords, func(w string) bool { return !committeeWord.MatchString(w) }):
		return Adapter{}, fmt.Errorf("%w: committee words %q", ErrInvalidConfig, c.CommitteeWords)
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
	object  bool
	hasDate bool
	date    string
	time    string
	timeErr bool
}

type entry struct {
	sourceID      string
	board         string
	title         string
	startsAt      time.Time
	cancelled     bool
	presentations bool
	pageURL       string
	agendaURL     string
	recordingURL  string
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

const agendaDue = 72 * time.Hour

func (a *Adapter) Meetings(ctx context.Context, window sources.Window) ([]sources.Meeting, error) {
	now := a.clock.Now()
	entries, err := a.entries(ctx, window, now)
	if err != nil {
		return nil, err
	}
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
		case !e.startsAt.After(now) && (e.recordingURL != "" || e.presentations):
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

func (a *Adapter) entries(ctx context.Context, window sources.Window, now time.Time) ([]entry, error) {
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
	decoded, wellFormed := 0, 0
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
		secs := sections(p.Content.Rendered)
		agenda, hasAgenda := agendaLink(secs)
		if word, ok := a.committee(agenda); hasAgenda && ok {
			a.logger.WarnContext(ctx, "vctc: dropped a board meeting post whose agenda names a committee", "post", p.ID, "title", t.full, "committee", word, "agenda", agenda.href)
			continue
		}
		m := decodeMeta(p.Meta)
		if p.ID > 0 && absolute(strings.TrimSpace(p.Link)) && m.hasDate {
			wellFormed++
		}
		e, err := a.entry(ctx, p, t, board, m, secs, now)
		switch {
		case err != nil && t.overlaps(days):
			a.logger.WarnContext(ctx, "vctc: skipped a board meeting post", "post", p.ID, "title", t.full, "error", err)
		case err != nil:
			a.logger.DebugContext(ctx, "vctc: skipped a board meeting post outside the window", "post", p.ID, "title", t.full, "error", err)
		default:
			out = append(out, e)
		}
	}
	if len(raws) > 0 && decoded == 0 {
		return nil, fmt.Errorf("%w: %s meetings: none of the %d posts decodes", ErrMalformed, a.cfg.Host, len(raws))
	}
	if decoded > 0 && wellFormed == 0 {
		return nil, fmt.Errorf("%w: %s meetings: none of the %d posts is a board meeting post with an id, a link and a meta._meeting_date field", ErrMalformed, a.cfg.Host, decoded)
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

func (a *Adapter) committee(l link) (string, bool) {
	words := strings.FieldsFunc(strings.ToLower(l.text+" "+l.href), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for _, c := range a.cfg.CommitteeWords {
		if slices.Contains(words, strings.ToLower(c)) {
			return c, true
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

func (a *Adapter) entry(ctx context.Context, p post, t title, board string, m meta, secs []section, now time.Time) (entry, error) {
	e := entry{sourceID: strconv.Itoa(p.ID), board: board, title: t.full, cancelled: t.cancelled, pageURL: strings.TrimSpace(p.Link)}
	switch {
	case p.ID <= 0:
		return e, fmt.Errorf("post id %d", p.ID)
	case !absolute(e.pageURL):
		return e, fmt.Errorf("link %q is not an absolute url", p.Link)
	case !m.object:
		return e, errors.New("no meta object")
	}
	agenda, hasAgenda := agendaLink(secs)
	day, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(m.date), a.loc)
	fromAgenda := false
	if err != nil {
		var ok bool
		day, ok = a.agendaDate(agenda, hasAgenda, t)
		if !ok {
			return e, fmt.Errorf("no meeting date (meta._meeting_date %q) and no single date of the title's month in the agenda link", m.date)
		}
		fromAgenda = true
		a.logger.WarnContext(ctx, "vctc: board meeting post has no meeting date; using the date in its agenda link at the configured start time", "post", p.ID, "title", t.full, "meeting_date", m.date, "source", agenda.href, "date", day.Format(time.DateOnly), "start_time", a.cfg.StartTime)
	}
	if fromAgenda {
		e.startsAt = time.Date(day.Year(), day.Month(), day.Day(), a.startHour, a.startMin, 0, 0, a.loc).UTC()
	} else {
		e.startsAt = a.start(ctx, p, day, m)
		if !t.agrees(day) {
			a.logger.WarnContext(ctx, "vctc: the post title names another date than the meeting date; using the meeting date", "post", p.ID, "title", t.full, "meeting_date", m.date)
		}
	}
	if hasAgenda {
		e.agendaURL = a.resolve(ctx, p, agenda, e.pageURL)
	}
	if e.agendaURL == "" && !e.cancelled && !e.startsAt.After(now.Add(agendaDue)) {
		a.logger.WarnContext(ctx, "vctc: board meeting post has no agenda link though the meeting is near or past", "post", p.ID, "title", t.full, "starts_at", e.startsAt.Format(time.RFC3339))
	}
	e.presentations = slices.ContainsFunc(secs, func(s section) bool { return s.presentations() && len(s.links) > 0 })
	return e, nil
}

func (a *Adapter) agendaDate(l link, ok bool, t title) (time.Time, bool) {
	if !ok || t.precision == noDate {
		return time.Time{}, false
	}
	var found []time.Time
	for _, d := range linkDates(l) {
		if t.agrees(d) {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		return time.Time{}, false
	}
	return time.Date(found[0].Year(), found[0].Month(), found[0].Day(), 0, 0, 0, 0, a.loc), true
}

func decodeMeta(raw json.RawMessage) meta {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return meta{}
	}
	m := meta{object: true}
	if v, ok := fields["_meeting_date"]; ok && json.Unmarshal(v, &m.date) == nil {
		m.hasDate = true
	}
	if v, ok := fields["_meeting_time"]; ok && json.Unmarshal(v, &m.time) != nil {
		m.timeErr = true
	}
	return m
}

var timeLayouts = []string{"15:04", "15:04:05", "3:04 PM", "3:04PM", "3:04 pm", "3:04pm"}

func (a *Adapter) start(ctx context.Context, p post, day time.Time, m meta) time.Time {
	hour, minute := a.startHour, a.startMin
	text := normalize(m.time)
	switch parsed, ok := parseClock(text); {
	case text == "" && !m.timeErr:
		a.logger.InfoContext(ctx, "vctc: board meeting post has no start time; using the configured start time", "post", p.ID, "start_time", a.cfg.StartTime)
	case !ok:
		a.logger.WarnContext(ctx, "vctc: board meeting post has an unreadable start time; using the configured start time", "post", p.ID, "time", m.time, "start_time", a.cfg.StartTime)
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

func agendaLink(secs []section) (link, bool) {
	for _, s := range secs {
		if !s.agenda() || len(s.links) == 0 {
			continue
		}
		for _, l := range s.links {
			lower := strings.ToLower(l.text + " " + l.href)
			if strings.Contains(lower, "amended") || strings.Contains(lower, "revised") {
				return l, true
			}
		}
		return s.links[0], true
	}
	return link{}, false
}

func (a *Adapter) resolve(ctx context.Context, p post, l link, pageURL string) string {
	base, _ := url.Parse(pageURL)
	ref, err := url.Parse(l.href)
	if err != nil {
		a.logger.WarnContext(ctx, "vctc: unreadable agenda link", "post", p.ID, "href", l.href, "error", err)
		return ""
	}
	resolved := base.ResolveReference(ref).String()
	if !absolute(resolved) {
		a.logger.WarnContext(ctx, "vctc: agenda link is not an http url", "post", p.ID, "href", l.href)
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
