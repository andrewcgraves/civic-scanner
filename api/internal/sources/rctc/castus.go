package rctc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
)

const (
	MaxListingBytes = 4 << 20
	castusMediaURL  = "https://dlttx48mxf9m3.cloudfront.net/outputs/%s/Default/HLS/out.m3u8"
	castusOpen      = "open"
)

var (
	castusVideoID   = regexp.MustCompile(`^[0-9a-f]{24}$`)
	castusTitleDate = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4}) meeting$`)
)

type Castus struct {
	ListingURL  string
	TitlePrefix string
}

func (c Castus) enabled() bool {
	return c.ListingURL != ""
}

func (c Castus) validate() error {
	if !c.enabled() {
		return nil
	}
	u, err := url.Parse(c.ListingURL)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil:
		return fmt.Errorf("%w: castus listing url %q", ErrInvalidConfig, c.ListingURL)
	case normalize(c.TitlePrefix) == "":
		return fmt.Errorf("%w: no castus title prefix", ErrInvalidConfig)
	}
	return nil
}

type castusListing struct {
	Videos []json.RawMessage `json:"videos"`
}

type castusVideo struct {
	ID       string `json:"_id"`
	Date     string `json:"date"`
	Access   string `json:"access"`
	Metadata struct {
		Filename string `json:"filename"`
	} `json:"metadata"`
}

type recording struct {
	id    string
	title string
	date  day
}

func (r recording) url() string {
	return fmt.Sprintf(castusMediaURL, r.id)
}

func (a *Adapter) recordings(ctx context.Context, window sources.Window) ([]recording, error) {
	c := a.cfg.Castus
	resp, err := a.fetcher.Fetch(ctx, fetch.Request{URL: c.ListingURL, MaxBytes: MaxListingBytes})
	if err != nil {
		return nil, fmt.Errorf("rctc castus listing: %w", err)
	}
	var l castusListing
	if err := json.Unmarshal(resp.Body, &l); err != nil {
		return nil, fmt.Errorf("%w: castus listing: %w", ErrMalformed, err)
	}
	if l.Videos == nil {
		return nil, fmt.Errorf("%w: castus listing: no videos array", ErrMalformed)
	}
	prefix := strings.ToLower(normalize(c.TitlePrefix))
	var out []recording
	dated := 0
	for i, raw := range l.Videos {
		var v castusVideo
		if err := json.Unmarshal(raw, &v); err != nil {
			a.logger.WarnContext(ctx, "rctc: skipped a castus video that does not decode", "index", i, "error", err)
			continue
		}
		title := normalize(v.Metadata.Filename)
		lower := strings.ToLower(title)
		rest, ok := strings.CutPrefix(lower, prefix+" ")
		if !ok {
			a.logger.DebugContext(ctx, "rctc: castus video is not a commission meeting", "video", v.ID, "title", title)
			continue
		}
		d, ok := titleDate(rest)
		if !ok {
			a.log(ctx, uploadedIn(v.Date, window), "rctc: skipped a commission video whose title has no readable date", "video", v.ID, "title", title, "uploaded", v.Date)
			continue
		}
		dated++
		switch {
		case !castusVideoID.MatchString(v.ID):
			a.logger.WarnContext(ctx, "rctc: skipped a commission video with an unexpected id", "video", v.ID, "title", title)
		case v.Access != castusOpen:
			a.logger.WarnContext(ctx, "rctc: skipped a commission video that is not public", "video", v.ID, "title", title, "access", v.Access)
		default:
			out = append(out, recording{id: v.ID, title: title, date: d})
		}
	}
	if len(l.Videos) > 0 && dated == 0 {
		return nil, fmt.Errorf("%w: castus listing: none of the %d videos is titled %q with a readable date", ErrMalformed, len(l.Videos), c.TitlePrefix+" M/D/YYYY Meeting")
	}
	return out, nil
}

func titleDate(rest string) (day, bool) {
	m := castusTitleDate.FindStringSubmatch(rest)
	if m == nil {
		return day{}, false
	}
	month, _ := strconv.Atoi(m[1])
	d, _ := strconv.Atoi(m[2])
	year, _ := strconv.Atoi(m[3])
	return civil(year, time.Month(month), d)
}

func uploadedIn(raw string, window sources.Window) bool {
	t, err := time.Parse(time.RFC3339, raw)
	return err != nil || window.Contains(t)
}

func (a *Adapter) matchRecordings(ctx context.Context, entries []entry, recs []recording, days windowDays, now time.Time) {
	byDate := map[day][]recording{}
	var dates []day
	for _, r := range recs {
		if _, ok := byDate[r.date]; !ok {
			dates = append(dates, r.date)
		}
		byDate[r.date] = append(byDate[r.date], r)
	}
	for _, d := range dates {
		rs := byDate[d]
		relevant := days.has(d)
		var matches []int
		for i, e := range entries {
			if e.date == d {
				matches = append(matches, i)
			}
		}
		switch {
		case len(rs) > 1:
			a.log(ctx, relevant, "rctc: several commission videos share one date; none is used", "date", d.String(), "videos", videoIDs(rs))
		case len(matches) == 0:
			a.log(ctx, relevant, "rctc: a commission video matches no board meeting on the page", "date", d.String(), "video", rs[0].id, "title", rs[0].title)
		case len(matches) > 1:
			a.log(ctx, relevant, "rctc: a commission video matches several board meetings on one date; none gets it", "date", d.String(), "video", rs[0].id, "meetings", len(matches))
		default:
			e := &entries[matches[0]]
			switch {
			case e.cancelled:
				a.log(ctx, relevant, "rctc: a commission video matches a cancelled board meeting; it is not used", "date", d.String(), "video", rs[0].id, "meeting", e.board.Name)
			case e.startsAt.After(now):
				a.log(ctx, relevant, "rctc: a commission video matches a board meeting that has not started; it is not used", "date", d.String(), "video", rs[0].id, "starts_at", e.startsAt.Format(time.RFC3339))
			default:
				e.recordingURL = rs[0].url()
			}
		}
	}
}

func videoIDs(rs []recording) string {
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.id
	}
	return strings.Join(ids, ",")
}
