package rctc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
)

var (
	ErrInvalidConfig = errors.New("rctc: invalid config")
	ErrMalformed     = errors.New("rctc: malformed response")
)

const (
	MaxPageBytes = 4 << 20
	agendaDue    = 72 * time.Hour
)

var (
	committeeWord = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	agendaDomain  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

type Config struct {
	PageURL        string
	AgendaDomain   string
	Boards         []Board
	CommitteeWords []string
	TimeZone       string
	StartTime      string
	Castus         Castus
}

type Board struct {
	Name    string
	Monthly bool
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
	a.logger = deps.Log().With("page", a.cfg.PageURL)
	return &a
}

func (c Config) adapter() (Adapter, error) {
	c.Boards = slices.Clone(c.Boards)
	c.CommitteeWords = slices.Clone(c.CommitteeWords)
	u, err := url.Parse(c.PageURL)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil:
		return Adapter{}, fmt.Errorf("%w: page url %q", ErrInvalidConfig, c.PageURL)
	case !agendaDomain.MatchString(c.AgendaDomain):
		return Adapter{}, fmt.Errorf("%w: agenda domain %q", ErrInvalidConfig, c.AgendaDomain)
	case len(c.Boards) == 0:
		return Adapter{}, fmt.Errorf("%w: no boards", ErrInvalidConfig)
	case slices.ContainsFunc(c.CommitteeWords, func(w string) bool { return !committeeWord.MatchString(w) }):
		return Adapter{}, fmt.Errorf("%w: committee words %q", ErrInvalidConfig, c.CommitteeWords)
	case c.TimeZone == "":
		return Adapter{}, fmt.Errorf("%w: no time zone", ErrInvalidConfig)
	}
	if err := c.Castus.validate(); err != nil {
		return Adapter{}, err
	}
	seen := map[string]bool{}
	for _, b := range c.Boards {
		key := strings.ToLower(normalize(b.Name))
		if key == "" || boardSlug(b.Name) == "" || seen[key] || parseName(b.Name).cleaned != normalize(b.Name) {
			return Adapter{}, fmt.Errorf("%w: boards %+v", ErrInvalidConfig, c.Boards)
		}
		seen[key] = true
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

type entry struct {
	index        int
	board        Board
	full         string
	date         day
	startsAt     time.Time
	cancelled    bool
	agendaURL    string
	recordingURL string
	sourceID     string
}

type windowDays struct {
	first day
	last  day
}

func (a *Adapter) days(window sources.Window) windowDays {
	toDay := func(t time.Time) day {
		l := t.In(a.loc)
		return day{year: l.Year(), month: l.Month(), day: l.Day()}
	}
	return windowDays{first: toDay(window.From), last: toDay(window.To)}
}

func before(x, y day) bool {
	return x.year < y.year || (x.year == y.year && (x.month < y.month || (x.month == y.month && x.day < y.day)))
}

func (w windowDays) has(d day) bool {
	return !before(d, w.first) && !before(w.last, d)
}

func (w windowDays) hasMonth(year int, month time.Month) bool {
	first := day{year: year, month: month, day: 1}
	last := day{year: year, month: month, day: time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()}
	return !before(last, w.first) && !before(w.last, first)
}

func (a *Adapter) Meetings(ctx context.Context, window sources.Window) ([]sources.Meeting, error) {
	resp, err := a.fetcher.Fetch(ctx, fetch.Request{URL: a.cfg.PageURL, MaxBytes: MaxPageBytes})
	if err != nil {
		return nil, fmt.Errorf("rctc %s: %w", a.cfg.PageURL, err)
	}
	tables, rows := parseTables(resp.Body)
	if tables == 0 {
		return nil, fmt.Errorf("%w: %s: no meeting table (a table headed DATE & TIME and MEETING)", ErrMalformed, a.cfg.PageURL)
	}
	base := a.cfg.PageURL
	if absolute(resp.FinalURL) {
		base = resp.FinalURL
	}
	now := a.clock.Now()
	days := a.days(window)

	var entries []entry
	listed, dated, boards := 0, 0, 0
	for i, r := range rows {
		if r.empty() {
			continue
		}
		listed++
		w := parseWhen(r.when)
		if w.hasDate {
			dated++
		}
		n, board, isBoard := a.match(r)
		if isBoard {
			boards++
		}
		e, ok := a.entry(ctx, i, r, w, n, board, isBoard, base, days, now)
		if ok {
			entries = append(entries, e)
		}
	}
	switch {
	case listed == 0:
		return nil, fmt.Errorf("%w: %s: the meeting tables list no meetings", ErrMalformed, a.cfg.PageURL)
	case dated == 0:
		return nil, fmt.Errorf("%w: %s: none of the %d listed meetings has a readable date", ErrMalformed, a.cfg.PageURL, listed)
	case boards == 0:
		return nil, fmt.Errorf("%w: %s: none of the %d listed meetings is a board meeting (%s)", ErrMalformed, a.cfg.PageURL, listed, a.boardNames())
	}

	entries = a.merge(ctx, entries, days)
	a.assignIDs(ctx, entries, days)
	if a.cfg.Castus.enabled() {
		recs, err := a.recordings(ctx, window)
		if err != nil {
			return nil, err
		}
		a.matchRecordings(ctx, entries, recs, days, now)
	}

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
			Title:        e.board.Name,
			StartsAt:     e.startsAt,
			Status:       status,
			PageURL:      a.cfg.PageURL,
			AgendaURL:    e.agendaURL,
			RecordingURL: e.recordingURL,
		})
	}
	slices.SortFunc(meetings, func(x, y sources.Meeting) int {
		return cmp.Or(x.StartsAt.Compare(y.StartsAt), strings.Compare(x.SourceID, y.SourceID))
	})
	return meetings, nil
}

func (a *Adapter) boardNames() string {
	names := make([]string, len(a.cfg.Boards))
	for i, b := range a.cfg.Boards {
		names[i] = b.Name
	}
	return strings.Join(names, ", ")
}

func (a *Adapter) board(n name) (Board, bool) {
	for _, b := range a.cfg.Boards {
		if strings.EqualFold(normalize(b.Name), n.cleaned) {
			return b, true
		}
	}
	return Board{}, false
}

func (a *Adapter) match(r row) (name, Board, bool) {
	whole := parseName(r.name)
	if b, ok := a.board(whole); ok {
		return whole, b, true
	}
	for _, l := range r.links {
		n := parseName(l.text)
		if b, ok := a.board(n); ok {
			n.marker = n.marker.merge(standaloneMarker(r.plain))
			for _, other := range r.links {
				n.marker = n.marker.merge(standaloneMarker(other.text))
			}
			return n, b, true
		}
	}
	if n := parseName(r.plain); r.plain != "" {
		if b, ok := a.board(n); ok {
			for _, other := range r.links {
				n.marker = n.marker.merge(standaloneMarker(other.text))
			}
			return n, b, true
		}
	}
	return whole, Board{}, false
}

func (a *Adapter) relevant(w when, r row, days windowDays) bool {
	if w.hasDate {
		return days.has(w.date)
	}
	for _, l := range r.links {
		if s, ok := linkSlug(l.href); ok {
			return days.hasMonth(s.year, s.month)
		}
	}
	return true
}

func (a *Adapter) log(ctx context.Context, relevant bool, msg string, args ...any) {
	if relevant {
		a.logger.WarnContext(ctx, msg, args...)
		return
	}
	a.logger.DebugContext(ctx, msg, args...)
}

func (a *Adapter) entry(ctx context.Context, i int, r row, w when, n name, board Board, isBoard bool, base string, days windowDays, now time.Time) (entry, bool) {
	relevant := a.relevant(w, r, days)
	if !isBoard {
		if looksLikeBoard(n.full) {
			a.log(ctx, relevant, "rctc: dropped a listed meeting that names the commission but is not a configured board meeting", "row", i, "meeting", n.full, "date", r.posted)
		} else {
			a.logger.DebugContext(ctx, "rctc: dropped a listed meeting that is not a board meeting", "row", i, "meeting", n.full, "date", r.posted)
		}
		return entry{}, false
	}
	l, hasLink := a.agendaLink(r.links)
	if hasLink {
		if word, ok := a.committee(l); ok {
			a.log(ctx, relevant, "rctc: dropped a board meeting whose agenda link names a committee", "row", i, "meeting", n.full, "date", r.posted, "agenda", redact(l.href), "committee", word)
			return entry{}, false
		}
	}
	if !w.hasDate {
		a.log(ctx, relevant, "rctc: skipped a board meeting without one readable date", "row", i, "meeting", n.full, "date", r.posted, "dates_found", w.dates)
		return entry{}, false
	}
	cancelled := w.called || n.marker == cancelMarker || (n.marker == rescheduleMarker && !w.movedFrom)
	e := entry{index: i, board: board, full: n.full, date: w.date, cancelled: cancelled}
	hour, minute := a.startHour, a.startMin
	switch {
	case w.hasTime:
		hour, minute = w.hour, w.minute
	case w.timeText:
		a.log(ctx, relevant, "rctc: board meeting has an unreadable start time; using the configured start time", "row", i, "meeting", n.full, "date", r.posted, "start_time", a.cfg.StartTime)
	case relevant:
		a.logger.InfoContext(ctx, "rctc: board meeting has no start time; using the configured start time", "row", i, "meeting", n.full, "date", r.posted, "start_time", a.cfg.StartTime)
	}
	e.startsAt = time.Date(w.date.year, w.date.month, w.date.day, hour, minute, 0, 0, a.loc).UTC()
	if hasLink {
		e.agendaURL = a.resolve(ctx, relevant, i, l, base)
		if s, ok := linkSlug(l.href); ok && (s.year != w.date.year || s.month != w.date.month) {
			a.log(ctx, relevant, "rctc: the agenda link names another month than the meeting date; using the meeting date", "row", i, "meeting", n.full, "date", r.posted, "agenda", redact(l.href))
		}
	}
	if e.agendaURL == "" && !e.cancelled && relevant && !e.startsAt.After(now.Add(agendaDue)) {
		a.logger.WarnContext(ctx, "rctc: board meeting has no agenda link though the meeting is near or past", "row", i, "meeting", n.full, "starts_at", e.startsAt.Format(time.RFC3339))
	}
	return e, true
}

func (a *Adapter) agendaLink(links []link) (link, bool) {
	var revised, board, named []link
	for _, l := range links {
		textWords := wordsOf(strings.ToLower(l.text))
		words := append(textWords, linkWords(l.href)...)
		if supporting(words) {
			continue
		}
		_, isBoard := a.board(parseName(l.text))
		saysAgenda := slices.Contains(words, "agenda")
		switch {
		case revisedWord(words) && saysAgenda:
			revised = append(revised, l)
		case isBoard:
			board = append(board, l)
		case slices.Contains(textWords, "agenda"):
			named = append(named, l)
		}
	}
	for _, group := range [][]link{revised, board, named} {
		if len(group) > 0 {
			return group[0], true
		}
	}
	return link{}, false
}

func (a *Adapter) committee(l link) (string, bool) {
	words := linkWords(l.href)
	for _, c := range a.cfg.CommitteeWords {
		if slices.Contains(words, strings.ToLower(c)) {
			return c, true
		}
	}
	return "", false
}

func (a *Adapter) resolve(ctx context.Context, relevant bool, i int, l link, base string) string {
	b, _ := url.Parse(base)
	ref, err := url.Parse(l.href)
	if err != nil {
		a.log(ctx, relevant, "rctc: unreadable agenda link", "row", i)
		return ""
	}
	resolved := b.ResolveReference(ref)
	resolved.Fragment = ""
	if !a.onAgendaHost(resolved) {
		a.log(ctx, relevant, "rctc: dropped an agenda link that is not https on the agency's domain", "row", i, "resolved", resolved.Redacted(), "domain", a.cfg.AgendaDomain)
		return ""
	}
	return resolved.String()
}

func (a *Adapter) onAgendaHost(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return host == a.cfg.AgendaDomain || strings.HasSuffix(host, "."+a.cfg.AgendaDomain)
}

func absolute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (a *Adapter) merge(ctx context.Context, entries []entry, days windowDays) []entry {
	slices.SortStableFunc(entries, func(x, y entry) int {
		return cmp.Or(x.startsAt.Compare(y.startsAt), strings.Compare(boardSlug(x.board.Name), boardSlug(y.board.Name)), x.index-y.index)
	})
	var out []entry
	for _, e := range entries {
		if n := len(out); n > 0 && out[n-1].board.Name == e.board.Name && out[n-1].startsAt.Equal(e.startsAt) {
			kept := &out[n-1]
			a.log(ctx, days.has(e.date), "rctc: the page lists one board meeting twice; keeping the first row", "meeting", e.board.Name, "date", e.date.String(), "rows", fmt.Sprintf("%d,%d", kept.index, e.index))
			if kept.agendaURL == "" {
				kept.agendaURL = e.agendaURL
			}
			continue
		}
		out = append(out, e)
	}
	return out
}

func (a *Adapter) assignIDs(ctx context.Context, entries []entry, days windowDays) {
	taken := map[string]bool{}
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(x, y int) int {
		return cmp.Compare(boolRank(entries[x].cancelled), boolRank(entries[y].cancelled))
	})
	for _, i := range order {
		e := &entries[i]
		prefix := boardSlug(e.board.Name) + "/"
		local := e.startsAt.In(a.loc)
		var candidates []string
		if e.board.Monthly {
			candidates = append(candidates, prefix+local.Format("2006-01"))
		}
		candidates = append(candidates, prefix+local.Format(time.DateOnly), prefix+local.Format("2006-01-02T1504"))
		for j, id := range candidates {
			if taken[id] {
				continue
			}
			e.sourceID = id
			taken[id] = true
			if j > 0 {
				a.log(ctx, days.has(e.date), "rctc: another meeting of this board already has the usual id; using a longer one", "meeting", e.board.Name, "date", e.date.String(), "source_id", id, "usual_id", candidates[0])
			}
			break
		}
	}
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func redact(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return u.Redacted()
}
