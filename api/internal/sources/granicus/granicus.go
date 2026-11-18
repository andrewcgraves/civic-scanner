package granicus

import (
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
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
	ErrInvalidConfig = errors.New("granicus: invalid config")
	ErrMalformed     = errors.New("granicus: malformed response")
)

type Config struct {
	Host       string
	ViewID     int
	BoardNames []string
	TimeZone   string
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
	c.BoardNames = slices.Clone(c.BoardNames)
	switch {
	case c.Host == "" || strings.ContainsAny(c.Host, "/?#:@ "):
		return c, nil, fmt.Errorf("%w: host %q", ErrInvalidConfig, c.Host)
	case c.ViewID <= 0:
		return c, nil, fmt.Errorf("%w: view id %d", ErrInvalidConfig, c.ViewID)
	case len(c.BoardNames) == 0:
		return c, nil, fmt.Errorf("%w: no board names", ErrInvalidConfig)
	case slices.ContainsFunc(c.BoardNames, func(n string) bool { return normalize(n) == "" }):
		return c, nil, fmt.Errorf("%w: board names %q", ErrInvalidConfig, c.BoardNames)
	case c.TimeZone == "":
		return c, nil, fmt.Errorf("%w: no time zone", ErrInvalidConfig)
	}
	loc, err := time.LoadLocation(c.TimeZone)
	if err != nil {
		return c, nil, fmt.Errorf("%w: time zone: %w", ErrInvalidConfig, err)
	}
	return c, loc, nil
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
}

type dateParts struct {
	Year   string `xml:"yr,attr"`
	Month  string `xml:"mo,attr"`
	Day    string `xml:"day,attr"`
	Hour   string `xml:"hr,attr"`
	Minute string `xml:"min,attr"`
}

type clip struct {
	id           int
	name         string
	cancelled    bool
	startsAt     time.Time
	agendaURL    string
	recordingURL string
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
	clips := map[int]*clip{}
	if err := a.collect(clips, agendas, func(c *clip, link string) { c.agendaURL = link }); err != nil {
		return nil, err
	}
	if err := a.collect(clips, videos, func(c *clip, link string) { c.recordingURL = link }); err != nil {
		return nil, err
	}

	ordered := make([]*clip, 0, len(clips))
	for _, c := range clips {
		if window.Contains(c.startsAt) {
			ordered = append(ordered, c)
		}
	}
	slices.SortFunc(ordered, func(x, y *clip) int {
		return cmp.Or(x.startsAt.Compare(y.startsAt), cmp.Compare(x.id, y.id))
	})
	now := a.clock.Now()
	meetings := make([]sources.Meeting, len(ordered))
	for i, c := range ordered {
		meetings[i] = c.meeting(now)
	}
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

func (a *Adapter) collect(clips map[int]*clip, items []item, set func(*clip, string)) error {
	for _, it := range items {
		name, cancelled := splitTitle(it.Title)
		if !a.board(name) {
			continue
		}
		parsed, err := a.parse(it, name, cancelled)
		if err != nil {
			return fmt.Errorf("%w: item %q: %w", ErrMalformed, it.Title, err)
		}
		c, ok := clips[parsed.id]
		if !ok {
			c = &parsed
			clips[parsed.id] = c
		}
		set(c, strings.TrimSpace(it.Link))
	}
	return nil
}

func (a *Adapter) board(name string) bool {
	return slices.ContainsFunc(a.cfg.BoardNames, func(n string) bool {
		return strings.EqualFold(normalize(n), name)
	})
}

func (a *Adapter) parse(it item, name string, cancelled bool) (clip, error) {
	link := strings.TrimSpace(it.Link)
	id, err := clipID(link)
	if err != nil {
		return clip{}, err
	}
	startsAt, err := a.startsAt(it.DateParts)
	if err != nil {
		return clip{}, err
	}
	return clip{id: id, name: name, cancelled: cancelled, startsAt: startsAt}, nil
}

func clipID(link string) (int, error) {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return 0, fmt.Errorf("link %q", link)
	}
	id, err := strconv.Atoi(u.Query().Get("clip_id"))
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("link %q has no clip id", link)
	}
	return id, nil
}

func (a *Adapter) startsAt(p *dateParts) (time.Time, error) {
	if p == nil {
		return time.Time{}, errors.New("no pubDateParts")
	}
	var n [5]int
	for i, raw := range []string{p.Year, p.Month, p.Day, p.Hour, p.Minute} {
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return time.Time{}, fmt.Errorf("pubDateParts %+v", *p)
		}
		n[i] = v
	}
	year, month, day, hour, minute := n[0], n[1], n[2], n[3], n[4]
	if month < 1 || month > 12 || day < 1 || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("pubDateParts %+v", *p)
	}
	local := time.Date(year, time.Month(month), day, hour, minute, 0, 0, a.loc)
	if local.Day() != day {
		return time.Time{}, fmt.Errorf("pubDateParts %+v", *p)
	}
	return local.UTC(), nil
}

func (c *clip) meeting(now time.Time) sources.Meeting {
	pageURL := c.recordingURL
	if pageURL == "" {
		pageURL = c.agendaURL
	}
	return sources.Meeting{
		SourceID:     strconv.Itoa(c.id),
		Title:        c.name,
		StartsAt:     c.startsAt,
		Status:       c.status(now),
		PageURL:      pageURL,
		AgendaURL:    c.agendaURL,
		RecordingURL: c.recordingURL,
	}
}

func (c *clip) status(now time.Time) sources.Status {
	switch {
	case c.cancelled:
		return sources.StatusCancelled
	case !c.startsAt.After(now) && c.recordingURL != "":
		return sources.StatusHeld
	default:
		return sources.StatusScheduled
	}
}

var (
	dateSuffix   = regexp.MustCompile(`\s+-\s+[A-Za-z]{3,9}\.? \d{1,2}, \d{4}$`)
	cancelSuffix = regexp.MustCompile(`(?i)^(.*?)[\s(\-]*\bcancell?ed\b.*$`)
)

func splitTitle(title string) (string, bool) {
	name := dateSuffix.ReplaceAllString(normalize(title), "")
	if m := cancelSuffix.FindStringSubmatch(name); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	return name, false
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
