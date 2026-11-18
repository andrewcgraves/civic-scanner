package legistar

import (
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

const (
	DefaultBaseURL  = "https://webapi.legistar.com"
	DefaultPageSize = 1000
)

var (
	ErrInvalidConfig = errors.New("legistar: invalid config")
	ErrMalformed     = errors.New("legistar: malformed response")
)

type Config struct {
	Client               string
	BodyIDs              []int
	TimeZone             string
	Granicus             Granicus
	FinalMinutesMeanHeld bool
	BaseURL              string
	PageSize             int
}

type Granicus struct {
	Host   string
	ViewID int
}

type Adapter struct {
	cfg     Config
	loc     *time.Location
	fetcher fetch.Fetcher
	clock   sources.Clock
	logger  *slog.Logger
}

func New(cfg Config, deps sources.Deps) (*Adapter, error) {
	cfg, loc, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	return newAdapter(cfg, loc, deps), nil
}

func Constructor(cfg Config) sources.Constructor {
	cfg, loc, err := cfg.resolve()
	if err != nil {
		panic(err)
	}
	return func(deps sources.Deps) sources.Adapter {
		return newAdapter(cfg, loc, deps)
	}
}

func newAdapter(cfg Config, loc *time.Location, deps sources.Deps) *Adapter {
	return &Adapter{cfg: cfg, loc: loc, fetcher: deps.Fetcher, clock: deps.Clock, logger: deps.Log()}
}

func (c Config) resolve() (Config, *time.Location, error) {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.PageSize == 0 {
		c.PageSize = DefaultPageSize
	}
	c.BodyIDs = slices.Clone(c.BodyIDs)
	switch {
	case c.Client == "" || url.PathEscape(c.Client) != c.Client:
		return c, nil, fmt.Errorf("%w: client %q", ErrInvalidConfig, c.Client)
	case len(c.BodyIDs) == 0:
		return c, nil, fmt.Errorf("%w: no body ids", ErrInvalidConfig)
	case slices.ContainsFunc(c.BodyIDs, func(id int) bool { return id <= 0 }):
		return c, nil, fmt.Errorf("%w: body ids %v", ErrInvalidConfig, c.BodyIDs)
	case c.PageSize < 0:
		return c, nil, fmt.Errorf("%w: page size %d", ErrInvalidConfig, c.PageSize)
	case !absoluteHTTP(c.BaseURL):
		return c, nil, fmt.Errorf("%w: base url %q", ErrInvalidConfig, c.BaseURL)
	case c.Granicus.Host != "" && (c.Granicus.ViewID <= 0 || strings.ContainsAny(c.Granicus.Host, "/?#:@ ")):
		return c, nil, fmt.Errorf("%w: granicus %+v", ErrInvalidConfig, c.Granicus)
	}
	if c.TimeZone == "" {
		return c, nil, fmt.Errorf("%w: no time zone", ErrInvalidConfig)
	}
	loc, err := time.LoadLocation(c.TimeZone)
	if err != nil {
		return c, nil, fmt.Errorf("%w: time zone: %w", ErrInvalidConfig, err)
	}
	return c, loc, nil
}

type event struct {
	EventID                int     `json:"EventId"`
	EventBodyID            int     `json:"EventBodyId"`
	EventBodyName          string  `json:"EventBodyName"`
	EventDate              string  `json:"EventDate"`
	EventTime              *string `json:"EventTime"`
	EventAgendaStatusName  *string `json:"EventAgendaStatusName"`
	EventMinutesStatusName *string `json:"EventMinutesStatusName"`
	EventAgendaFile        *string `json:"EventAgendaFile"`
	EventMinutesFile       *string `json:"EventMinutesFile"`
	EventInSiteURL         *string `json:"EventInSiteURL"`
	EventMedia             *string `json:"EventMedia"`
	EventVideoPath         *string `json:"EventVideoPath"`
	EventComment           *string `json:"EventComment"`
}

type item struct {
	event
	err error
}

func (a *Adapter) Meetings(ctx context.Context, window sources.Window) ([]sources.Meeting, error) {
	items, err := a.items(ctx, window)
	if err != nil {
		return nil, err
	}
	now := a.clock.Now()
	meetings := make([]sources.Meeting, 0, len(items))
	unreadable := 0
	for _, it := range items {
		if it.err == nil && !a.board(ctx, it.event) {
			continue
		}
		m, err := sources.Meeting{}, it.err
		if err == nil {
			m, err = a.meeting(ctx, it.event, now)
		}
		if err != nil {
			unreadable++
			a.logger.WarnContext(ctx, "legistar: skipped an unreadable board event", "client", a.cfg.Client, "event_id", it.EventID, "error", err)
			continue
		}
		meetings = append(meetings, m)
	}
	if unreadable > 0 && len(meetings) == 0 {
		return nil, fmt.Errorf("%w: %s: all %d board events in the window are unreadable", ErrMalformed, a.cfg.Client, unreadable)
	}
	return meetings, nil
}

func (a *Adapter) board(ctx context.Context, e event) bool {
	if !slices.Contains(a.cfg.BodyIDs, e.EventBodyID) {
		if strings.Contains(strings.ToLower(e.EventBodyName), "board") {
			a.logger.InfoContext(ctx, "legistar: dropped an event of a body that is not a configured board", "client", a.cfg.Client, "event_id", e.EventID, "body_id", e.EventBodyID, "body", e.EventBodyName)
		}
		return false
	}
	if strings.EqualFold(text(e.EventAgendaStatusName), "hidden") {
		a.logger.InfoContext(ctx, "legistar: dropped a hidden board event", "client", a.cfg.Client, "event_id", e.EventID, "comment", text(e.EventComment))
		return false
	}
	return true
}

func (a *Adapter) items(ctx context.Context, window sources.Window) ([]item, error) {
	var all []item
	seen := map[int]bool{}
	for skip := 0; ; skip += a.cfg.PageSize {
		page, err := a.page(ctx, window, skip)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, it := range page {
			if it.err != nil {
				all = append(all, it)
				continue
			}
			if seen[it.EventID] {
				continue
			}
			seen[it.EventID] = true
			all = append(all, it)
			added++
		}
		if len(page) < a.cfg.PageSize {
			return all, nil
		}
		if added == 0 {
			return nil, fmt.Errorf("%w: page at offset %d repeats earlier events", ErrMalformed, skip)
		}
	}
}

func (a *Adapter) page(ctx context.Context, window sources.Window, skip int) ([]item, error) {
	target := a.eventsURL(window, skip)
	resp, err := a.fetcher.Fetch(ctx, fetch.Request{URL: target})
	if err != nil {
		return nil, fmt.Errorf("legistar %s events: %w", a.cfg.Client, err)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(resp.Body, &raws); err != nil {
		return nil, fmt.Errorf("%w: %s events: %w", ErrMalformed, a.cfg.Client, err)
	}
	page := make([]item, len(raws))
	for i, raw := range raws {
		var it item
		if err := json.Unmarshal(raw, &it.event); err != nil {
			it.err = err
		} else if it.EventID <= 0 {
			it.err = errors.New("event without an id")
		}
		page[i] = it
	}
	return page, nil
}

func (a *Adapter) eventsURL(window sources.Window, skip int) string {
	const dateLayout = "2006-01-02"
	bodies := make([]string, len(a.cfg.BodyIDs))
	for i, id := range a.cfg.BodyIDs {
		bodies[i] = "EventBodyId eq " + strconv.Itoa(id)
	}
	filter := fmt.Sprintf("EventDate ge datetime'%s' and EventDate le datetime'%s' and (%s)",
		window.From.In(a.loc).Format(dateLayout), window.To.In(a.loc).Format(dateLayout), strings.Join(bodies, " or "))
	q := url.Values{
		"$filter":  {filter},
		"$orderby": {"EventDate,EventId"},
		"$skip":    {strconv.Itoa(skip)},
		"$top":     {strconv.Itoa(a.cfg.PageSize)},
	}
	return strings.TrimRight(a.cfg.BaseURL, "/") + "/v1/" + a.cfg.Client + "/events?" + q.Encode()
}

func (a *Adapter) meeting(ctx context.Context, e event, now time.Time) (sources.Meeting, error) {
	title := strings.TrimSpace(e.EventBodyName)
	if title == "" {
		return sources.Meeting{}, errors.New("no body name")
	}
	startsAt, err := a.startsAt(ctx, e)
	if err != nil {
		return sources.Meeting{}, err
	}
	pageURL := text(e.EventInSiteURL)
	if !absoluteHTTP(pageURL) {
		return sources.Meeting{}, fmt.Errorf("page url %q", pageURL)
	}
	agendaURL := text(e.EventAgendaFile)
	if agendaURL != "" && !absoluteHTTP(agendaURL) {
		return sources.Meeting{}, fmt.Errorf("agenda url %q", agendaURL)
	}
	recordingURL, err := a.recordingURL(e)
	if err != nil {
		return sources.Meeting{}, err
	}
	cancelled := cancelled(e)
	if cancelled || startsAt.After(now) {
		recordingURL = ""
	}
	return sources.Meeting{
		SourceID:     strconv.Itoa(e.EventID),
		Title:        title,
		StartsAt:     startsAt,
		Status:       a.status(e, cancelled, startsAt, now),
		PageURL:      pageURL,
		AgendaURL:    agendaURL,
		RecordingURL: recordingURL,
	}, nil
}

var (
	timeLayouts    = []string{"3:04 PM", "3:04PM", "15:04"}
	agendaFileDate = regexp.MustCompile(`_(\d{2}-\d{2}-\d{2})_Agenda\.pdf$`)
)

func (a *Adapter) startsAt(ctx context.Context, e event) (time.Time, error) {
	date, err := time.Parse("2006-01-02T15:04:05", e.EventDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("event date %q", e.EventDate)
	}
	a.crossCheckAgendaDate(ctx, e, date)
	hour, minute := 0, 0
	clock := strings.Join(strings.Fields(strings.ToUpper(text(e.EventTime))), " ")
	parsed := clock == ""
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, clock); err == nil {
			hour, minute, parsed = t.Hour(), t.Minute(), true
			break
		}
	}
	if !parsed {
		a.logger.WarnContext(ctx, "legistar: unreadable event time, using local midnight", "client", a.cfg.Client, "event_id", e.EventID, "time", text(e.EventTime))
	}
	return time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, a.loc).UTC(), nil
}

func (a *Adapter) crossCheckAgendaDate(ctx context.Context, e event, date time.Time) {
	m := agendaFileDate.FindStringSubmatch(text(e.EventAgendaFile))
	if m == nil {
		return
	}
	named, err := time.Parse("06-01-02", m[1])
	if err != nil || named.Equal(date) {
		return
	}
	a.logger.WarnContext(ctx, "legistar: agenda file name has a different date than the event, using the event date", "client", a.cfg.Client, "event_id", e.EventID, "event_date", date.Format(time.DateOnly), "agenda_file", text(e.EventAgendaFile))
}

func (a *Adapter) recordingURL(e event) (string, error) {
	if path := text(e.EventVideoPath); path != "" {
		if !absoluteHTTP(path) {
			return "", fmt.Errorf("video path %q", path)
		}
		return path, nil
	}
	clip := text(e.EventMedia)
	if clip == "" || a.cfg.Granicus.Host == "" {
		return "", nil
	}
	return fmt.Sprintf("https://%s/MediaPlayer.php?view_id=%d&clip_id=%s", a.cfg.Granicus.Host, a.cfg.Granicus.ViewID, url.QueryEscape(clip)), nil
}

func cancelled(e event) bool {
	return strings.Contains(strings.ToLower(text(e.EventAgendaStatusName)), "cancel") ||
		strings.Contains(strings.ToLower(text(e.EventComment)), "cancel")
}

func (a *Adapter) status(e event, cancelled bool, startsAt, now time.Time) sources.Status {
	switch {
	case cancelled:
		return sources.StatusCancelled
	case !startsAt.After(now) && a.heldEvidence(e):
		return sources.StatusHeld
	default:
		return sources.StatusScheduled
	}
}

func (a *Adapter) heldEvidence(e event) bool {
	return text(e.EventMedia) != "" ||
		text(e.EventVideoPath) != "" ||
		text(e.EventMinutesFile) != "" ||
		(a.cfg.FinalMinutesMeanHeld && strings.EqualFold(text(e.EventMinutesStatusName), "final"))
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func absoluteHTTP(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
