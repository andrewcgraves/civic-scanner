package primegov

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
	ErrInvalidConfig = errors.New("primegov: invalid config")
	ErrMalformed     = errors.New("primegov: malformed response")
)

var primeGovHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+primegov\.com$`)

const (
	MaxListBytes    = 16 << 20
	maxArchiveYears = 5
	agendaDue       = 72 * time.Hour
)

type Config struct {
	Host              string
	AgendaHost        string
	CommitteeIDs      []int
	BoardName         string
	AgendaTemplates   []string
	EvidenceTemplates []string
	CancelTemplates   []string
	PageURL           string
	TimeZone          string
	StartTime         string
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
	c.CommitteeIDs = slices.Clone(c.CommitteeIDs)
	c.AgendaTemplates = slices.Clone(c.AgendaTemplates)
	c.EvidenceTemplates = slices.Clone(c.EvidenceTemplates)
	c.CancelTemplates = slices.Clone(c.CancelTemplates)
	blank := func(s string) bool { return normalize(s) == "" }
	switch {
	case !primeGovHost.MatchString(c.Host):
		return Adapter{}, fmt.Errorf("%w: host %q", ErrInvalidConfig, c.Host)
	case !primeGovHost.MatchString(c.AgendaHost):
		return Adapter{}, fmt.Errorf("%w: agenda host %q is not a lower-case host under primegov.com", ErrInvalidConfig, c.AgendaHost)
	case len(c.CommitteeIDs) == 0 || slices.ContainsFunc(c.CommitteeIDs, func(id int) bool { return id <= 0 }):
		return Adapter{}, fmt.Errorf("%w: committee ids %v", ErrInvalidConfig, c.CommitteeIDs)
	case blank(c.BoardName) || parseTitle(c.BoardName).name != normalize(c.BoardName):
		return Adapter{}, fmt.Errorf("%w: board name %q", ErrInvalidConfig, c.BoardName)
	case len(c.AgendaTemplates) == 0 || slices.ContainsFunc(c.AgendaTemplates, blank):
		return Adapter{}, fmt.Errorf("%w: agenda templates %q", ErrInvalidConfig, c.AgendaTemplates)
	case slices.ContainsFunc(c.EvidenceTemplates, blank) || slices.ContainsFunc(c.CancelTemplates, blank):
		return Adapter{}, fmt.Errorf("%w: evidence templates %q, cancel templates %q", ErrInvalidConfig, c.EvidenceTemplates, c.CancelTemplates)
	case !absolute(c.PageURL):
		return Adapter{}, fmt.Errorf("%w: page url %q", ErrInvalidConfig, c.PageURL)
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

func (a *Adapter) UpcomingURL() string {
	return "https://" + a.cfg.Host + "/api/v2/PublicPortal/ListUpcomingMeetings"
}

func (a *Adapter) ArchivedURL(year int) string {
	return "https://" + a.cfg.Host + "/api/v2/PublicPortal/ListArchivedMeetings?year=" + strconv.Itoa(year)
}

func (a *Adapter) AgendaURL(templateID, outputType int) string {
	return fmt.Sprintf("https://%s/Public/CompiledDocument?meetingTemplateId=%d&compileOutputType=%d", a.cfg.AgendaHost, templateID, outputType)
}

type item struct {
	ID          int               `json:"id"`
	CommitteeID int               `json:"committeeId"`
	TypeID      int               `json:"meetingTypeId"`
	DateTime    string            `json:"dateTime"`
	Date        string            `json:"date"`
	Time        string            `json:"time"`
	Title       string            `json:"title"`
	VideoURL    string            `json:"videoUrl"`
	Documents   []json.RawMessage `json:"documentList"`
}

type document struct {
	ID          int    `json:"id"`
	TemplateID  int    `json:"templateId"`
	Name        string `json:"templateName"`
	OutputType  int    `json:"compileOutputType"`
	PublishDate string `json:"publishDate"`
}

func (it item) wellFormed() bool {
	return it.ID > 0 && it.CommitteeID > 0 && strings.TrimSpace(it.Title) != "" &&
		(strings.TrimSpace(it.DateTime) != "" || strings.TrimSpace(it.Date) != "")
}

type note struct {
	level slog.Level
	msg   string
	args  []any
}

func (a *Adapter) Meetings(ctx context.Context, window sources.Window) ([]sources.Meeting, error) {
	now := a.clock.Now()
	items, err := a.items(ctx, window, now)
	if err != nil {
		return nil, err
	}
	var meetings []sources.Meeting
	seen, read := 0, 0
	for _, it := range items {
		t := parseTitle(it.Title)
		startsAt, notes, startErr := a.start(it)
		inWindow := startErr != nil || window.Contains(startsAt)
		if !a.board(it, t) {
			a.logDropped(ctx, it, t, inWindow)
			continue
		}
		if !inWindow {
			a.logger.DebugContext(ctx, "primegov: board meeting outside the window", "meeting", it.ID, "title", t.full, "starts_at", startsAt.Format(time.RFC3339))
			continue
		}
		docs := a.documents(ctx, it)
		if darkPlaceholder(t, docs) {
			a.logger.DebugContext(ctx, "primegov: left out a dark month without documents", "meeting", it.ID, "title", t.full)
			continue
		}
		seen++
		if startErr != nil {
			a.logger.WarnContext(ctx, "primegov: skipped a board meeting without a readable date", "meeting", it.ID, "title", t.full, "error", startErr)
			continue
		}
		for _, n := range notes {
			a.logger.Log(ctx, n.level, n.msg, append([]any{"meeting", it.ID, "title", t.full}, n.args...)...)
		}
		read++
		meetings = append(meetings, a.meeting(ctx, it, t, docs, startsAt, now))
	}
	if seen > 0 && read == 0 {
		return nil, fmt.Errorf("%w: %s: all %d board meetings in the window are unreadable", ErrMalformed, a.cfg.Host, seen)
	}
	a.warnDuplicates(ctx, meetings)
	slices.SortFunc(meetings, func(x, y sources.Meeting) int {
		return cmp.Or(x.StartsAt.Compare(y.StartsAt), strings.Compare(x.SourceID, y.SourceID))
	})
	return meetings, nil
}

func (a *Adapter) items(ctx context.Context, window sources.Window, now time.Time) ([]item, error) {
	targets := []string{a.UpcomingURL()}
	for _, year := range a.archiveYears(ctx, window, now) {
		targets = append(targets, a.ArchivedURL(year))
	}
	byID := map[int]int{}
	from := map[int]string{}
	var out []item
	total, decoded, wellFormed, board := 0, 0, 0, 0
	for _, target := range targets {
		raws, err := a.list(ctx, target)
		if err != nil {
			return nil, err
		}
		total += len(raws)
		for i, raw := range raws {
			var it item
			if err := json.Unmarshal(raw, &it); err != nil {
				a.logger.WarnContext(ctx, "primegov: skipped a meeting that does not decode", "url", target, "index", i, "error", err)
				continue
			}
			decoded++
			if !it.wellFormed() {
				a.logger.WarnContext(ctx, "primegov: skipped a meeting without an id, committee, title or date", "url", target, "index", i, "meeting", it.ID)
				continue
			}
			wellFormed++
			if slices.Contains(a.cfg.CommitteeIDs, it.CommitteeID) {
				board++
			}
			if at, ok := byID[it.ID]; ok {
				out[at] = a.duplicate(ctx, out[at], it, from[it.ID] == target, target)
				from[it.ID] = target
				continue
			}
			byID[it.ID] = len(out)
			from[it.ID] = target
			out = append(out, it)
		}
	}
	switch {
	case total > 0 && decoded == 0:
		return nil, fmt.Errorf("%w: %s: none of the %d meetings decodes", ErrMalformed, a.cfg.Host, total)
	case decoded > 0 && wellFormed == 0:
		return nil, fmt.Errorf("%w: %s: none of the %d meetings has an id, a committee id, a title and a date", ErrMalformed, a.cfg.Host, decoded)
	case wellFormed > 0 && board == 0:
		return nil, fmt.Errorf("%w: %s: none of the %d meetings belongs to committee %v; the board's committee id may have changed", ErrMalformed, a.cfg.Host, wellFormed, a.cfg.CommitteeIDs)
	}
	return out, nil
}

func (a *Adapter) duplicate(ctx context.Context, kept, next item, sameList bool, target string) item {
	keptBoard, nextBoard := a.board(kept, parseTitle(kept.Title)), a.board(next, parseTitle(next.Title))
	chosen := next
	if keptBoard && !nextBoard {
		chosen = kept
	}
	if sameList || keptBoard != nextBoard {
		a.logger.WarnContext(ctx, "primegov: one meeting id appears twice; keeping the board meeting, else the later entry", "url", target, "meeting", next.ID, "titles", kept.Title+" | "+next.Title, "kept", chosen.Title)
	}
	return chosen
}

func (a *Adapter) archiveYears(ctx context.Context, window sources.Window, now time.Time) []int {
	last := window.To
	if now.Before(last) {
		last = now
	}
	first, final := window.From.In(a.loc).Year(), last.In(a.loc).Year()
	if final-first >= maxArchiveYears {
		a.logger.InfoContext(ctx, "primegov: window reaches further back than the archive is read", "from", window.From.Format(time.RFC3339), "years", maxArchiveYears)
		first = final - maxArchiveYears + 1
	}
	var years []int
	for y := first; y <= final; y++ {
		years = append(years, y)
	}
	return years
}

func (a *Adapter) list(ctx context.Context, target string) ([]json.RawMessage, error) {
	resp, err := a.fetcher.Fetch(ctx, fetch.Request{URL: target, MaxBytes: MaxListBytes})
	if err != nil {
		return nil, fmt.Errorf("primegov %s meetings: %w", a.cfg.Host, err)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(resp.Body, &raws); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMalformed, target, err)
	}
	return raws, nil
}

func (a *Adapter) board(it item, t title) bool {
	return slices.Contains(a.cfg.CommitteeIDs, it.CommitteeID) && !t.test && strings.EqualFold(t.name, normalize(a.cfg.BoardName))
}

func darkPlaceholder(t title, docs []document) bool {
	return t.dark && len(docs) == 0
}

func (a *Adapter) logDropped(ctx context.Context, it item, t title, inWindow bool) {
	inCommittee := slices.Contains(a.cfg.CommitteeIDs, it.CommitteeID)
	named := strings.Contains(strings.ToLower(t.full), strings.ToLower(normalize(a.cfg.BoardName))) || strings.Contains(strings.ToLower(t.full), "board")
	args := []any{"meeting", it.ID, "title", t.full, "committee", it.CommitteeID, "type", it.TypeID}
	switch {
	case !inWindow:
		if inCommittee || named {
			a.logger.DebugContext(ctx, "primegov: dropped a meeting outside the window that is not a board meeting", args...)
		}
	case inCommittee:
		a.logger.WarnContext(ctx, "primegov: dropped a meeting of the board's committee whose title is not the board's", args...)
	case named:
		a.logger.InfoContext(ctx, "primegov: dropped a meeting that names the board but is not of the board's committee", args...)
	}
}

var (
	dateTimeLayouts = []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04"}
	dateLayouts     = []string{"Jan 02, 2006", "Jan 2, 2006", "January 2, 2006", time.DateOnly}
	clockLayouts    = []string{"03:04 PM", "3:04 PM", "3:04PM", "15:04"}
)

func parseIn(text string, layouts []string, loc *time.Location) (time.Time, bool) {
	text = normalize(text)
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, text, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (a *Adapter) start(it item) (time.Time, []note, error) {
	var notes []note
	clockText := strings.ToUpper(normalize(it.Time))
	clock, clockOK := parseIn(clockText, clockLayouts, time.UTC)
	stated, statedOK := parseIn(it.Date, dateLayouts, a.loc)
	at, ok := parseIn(it.DateTime, dateTimeLayouts, a.loc)
	if ok {
		if statedOK && !sameDay(stated, at) {
			notes = append(notes, note{slog.LevelWarn, "primegov: the meeting's date field names another day than its dateTime; using dateTime", []any{"date", it.Date, "date_time", it.DateTime}})
		}
		midnight := at.Hour() == 0 && at.Minute() == 0
		if clockOK && (clock.Hour() != at.Hour() || clock.Minute() != at.Minute()) {
			if midnight {
				notes = append(notes, note{slog.LevelWarn, "primegov: the meeting's dateTime is at midnight but its time field states a time; using the time field", []any{"time", it.Time, "date_time", it.DateTime}})
				return time.Date(at.Year(), at.Month(), at.Day(), clock.Hour(), clock.Minute(), 0, 0, a.loc).UTC(), notes, nil
			}
			notes = append(notes, note{slog.LevelWarn, "primegov: the meeting's time field names another time than its dateTime; using dateTime", []any{"time", it.Time, "date_time", it.DateTime}})
		}
		if clockText != "" || !midnight {
			return at.UTC(), notes, nil
		}
		notes = append(notes, note{slog.LevelInfo, "primegov: board meeting has no start time; using the configured start time", []any{"date_time", it.DateTime, "start_time", a.cfg.StartTime}})
		return a.at(at), notes, nil
	}
	if !statedOK {
		return time.Time{}, nil, fmt.Errorf("no readable date (dateTime %q, date %q)", it.DateTime, it.Date)
	}
	switch {
	case clockOK:
		return time.Date(stated.Year(), stated.Month(), stated.Day(), clock.Hour(), clock.Minute(), 0, 0, a.loc).UTC(), notes, nil
	case clockText == "":
		notes = append(notes, note{slog.LevelInfo, "primegov: board meeting has no start time; using the configured start time", []any{"date", it.Date, "start_time", a.cfg.StartTime}})
	default:
		notes = append(notes, note{slog.LevelWarn, "primegov: board meeting has an unreadable start time; using the configured start time", []any{"time", it.Time, "start_time", a.cfg.StartTime}})
	}
	return a.at(stated), notes, nil
}

func (a *Adapter) at(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), a.startHour, a.startMin, 0, 0, a.loc).UTC()
}

func sameDay(x, y time.Time) bool {
	return x.Year() == y.Year() && x.Month() == y.Month() && x.Day() == y.Day()
}

func (a *Adapter) meeting(ctx context.Context, it item, t title, docs []document, startsAt, now time.Time) sources.Meeting {
	cancelled := t.cancelled || t.postponed || t.dark || hasTemplate(docs, a.cfg.CancelTemplates)
	status := sources.StatusScheduled
	switch {
	case cancelled:
		status = sources.StatusCancelled
	case !startsAt.After(now) && (hasTemplate(docs, a.cfg.EvidenceTemplates) || strings.TrimSpace(it.VideoURL) != ""):
		status = sources.StatusHeld
	}
	agendaURL := a.agenda(docs)
	if agendaURL == "" && !cancelled && !startsAt.After(now.Add(agendaDue)) {
		a.logger.WarnContext(ctx, "primegov: board meeting has no agenda document though the meeting is near or past", "meeting", it.ID, "title", t.full, "starts_at", startsAt.Format(time.RFC3339), "templates", templateNames(docs))
	}
	return sources.Meeting{
		SourceID:  strconv.Itoa(it.ID),
		Title:     t.display,
		StartsAt:  startsAt,
		Status:    status,
		PageURL:   a.cfg.PageURL,
		AgendaURL: agendaURL,
	}
}

func (a *Adapter) documents(ctx context.Context, it item) []document {
	var docs []document
	for i, raw := range it.Documents {
		var d document
		if err := json.Unmarshal(raw, &d); err != nil {
			a.logger.WarnContext(ctx, "primegov: skipped a document that does not decode", "meeting", it.ID, "index", i, "error", err)
			continue
		}
		d.Name = normalize(d.Name)
		docs = append(docs, d)
	}
	return docs
}

func hasTemplate(docs []document, names []string) bool {
	return slices.ContainsFunc(docs, func(d document) bool {
		return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(d.Name, normalize(n)) })
	})
}

func templateNames(docs []document) string {
	names := make([]string, len(docs))
	for i, d := range docs {
		names[i] = d.Name
	}
	return strings.Join(names, ", ")
}

func (a *Adapter) agenda(docs []document) string {
	for _, name := range a.cfg.AgendaTemplates {
		var best *document
		for i := range docs {
			d := &docs[i]
			if !strings.EqualFold(d.Name, normalize(name)) || d.TemplateID <= 0 {
				continue
			}
			if best == nil || newer(*d, *best) {
				best = d
			}
		}
		if best != nil {
			return a.AgendaURL(best.TemplateID, cmp.Or(max(best.OutputType, 0), 1))
		}
	}
	return ""
}

func newer(x, y document) bool {
	px, _ := parseIn(x.PublishDate, dateTimeLayouts, time.UTC)
	py, _ := parseIn(y.PublishDate, dateTimeLayouts, time.UTC)
	if c := px.Compare(py); c != 0 {
		return c > 0
	}
	return x.TemplateID > y.TemplateID
}

func absolute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (a *Adapter) warnDuplicates(ctx context.Context, meetings []sources.Meeting) {
	seen := map[string]string{}
	for _, m := range meetings {
		day := m.StartsAt.In(a.loc).Format(time.DateOnly)
		if other, ok := seen[day]; ok {
			a.logger.WarnContext(ctx, "primegov: two board meetings on one day; both are stored", "date", day, "meetings", other+","+m.SourceID)
			continue
		}
		seen[day] = m.SourceID
	}
}
