package legistar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
	Client   string
	BodyIDs  []int
	TimeZone string
	Granicus Granicus
	BaseURL  string
	PageSize int
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
}

func New(cfg Config, deps sources.Deps) (*Adapter, error) {
	cfg, loc, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	return &Adapter{cfg: cfg, loc: loc, fetcher: deps.Fetcher, clock: deps.Clock}, nil
}

func Constructor(cfg Config) sources.Constructor {
	cfg, loc, err := cfg.resolve()
	if err != nil {
		panic(err)
	}
	return func(deps sources.Deps) sources.Adapter {
		return &Adapter{cfg: cfg, loc: loc, fetcher: deps.Fetcher, clock: deps.Clock}
	}
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
}

func (a *Adapter) Meetings(ctx context.Context, window sources.Window) ([]sources.Meeting, error) {
	events, err := a.events(ctx, window)
	if err != nil {
		return nil, err
	}
	now := a.clock.Now()
	meetings := make([]sources.Meeting, 0, len(events))
	for _, e := range events {
		if !slices.Contains(a.cfg.BodyIDs, e.EventBodyID) {
			continue
		}
		m, err := a.meeting(e, now)
		if err != nil {
			return nil, fmt.Errorf("%w: event %d: %w", ErrMalformed, e.EventID, err)
		}
		meetings = append(meetings, m)
	}
	return meetings, nil
}

func (a *Adapter) events(ctx context.Context, window sources.Window) ([]event, error) {
	var all []event
	seen := map[int]bool{}
	for skip := 0; ; skip += a.cfg.PageSize {
		page, err := a.page(ctx, window, skip)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, e := range page {
			if e.EventID <= 0 {
				return nil, fmt.Errorf("%w: event without an id", ErrMalformed)
			}
			if seen[e.EventID] {
				continue
			}
			seen[e.EventID] = true
			all = append(all, e)
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

func (a *Adapter) page(ctx context.Context, window sources.Window, skip int) ([]event, error) {
	target := a.eventsURL(window, skip)
	resp, err := a.fetcher.Fetch(ctx, fetch.Request{URL: target})
	if err != nil {
		return nil, fmt.Errorf("legistar %s events: %w", a.cfg.Client, err)
	}
	var page []event
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		return nil, fmt.Errorf("%w: %s events: %w", ErrMalformed, a.cfg.Client, err)
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

func (a *Adapter) meeting(e event, now time.Time) (sources.Meeting, error) {
	title := strings.TrimSpace(e.EventBodyName)
	if title == "" {
		return sources.Meeting{}, errors.New("no body name")
	}
	startsAt, err := a.startsAt(e)
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
	return sources.Meeting{
		SourceID:     strconv.Itoa(e.EventID),
		Title:        title,
		StartsAt:     startsAt,
		Status:       status(e, startsAt, now),
		PageURL:      pageURL,
		AgendaURL:    agendaURL,
		RecordingURL: recordingURL,
	}, nil
}

var timeLayouts = []string{"3:04 PM", "3:04PM", "15:04"}

func (a *Adapter) startsAt(e event) (time.Time, error) {
	date, err := time.Parse("2006-01-02T15:04:05", e.EventDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("event date %q", e.EventDate)
	}
	hour, minute := 0, 0
	clock := strings.ToUpper(strings.TrimSpace(text(e.EventTime)))
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, clock); err == nil {
			hour, minute = t.Hour(), t.Minute()
			break
		}
	}
	return time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, a.loc).UTC(), nil
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

func status(e event, startsAt, now time.Time) sources.Status {
	switch {
	case strings.Contains(strings.ToLower(text(e.EventAgendaStatusName)), "cancel"):
		return sources.StatusCancelled
	case !startsAt.After(now) && heldEvidence(e):
		return sources.StatusHeld
	default:
		return sources.StatusScheduled
	}
}

func heldEvidence(e event) bool {
	return text(e.EventMedia) != "" ||
		text(e.EventVideoPath) != "" ||
		text(e.EventMinutesFile) != "" ||
		strings.EqualFold(text(e.EventMinutesStatusName), "final")
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
