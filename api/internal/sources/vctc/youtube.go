package vctc

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
)

type YouTube struct {
	ChannelID   string
	TitlePrefix string
	MaxDelay    time.Duration
}

var (
	channelIDPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	videoIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
)

func (y YouTube) enabled() bool {
	return y.ChannelID != ""
}

func (y YouTube) validate() error {
	switch {
	case !y.enabled():
		return nil
	case !channelIDPattern.MatchString(y.ChannelID):
		return fmt.Errorf("%w: youtube channel id %q", ErrInvalidConfig, y.ChannelID)
	case normalize(y.TitlePrefix) == "":
		return fmt.Errorf("%w: no youtube title prefix", ErrInvalidConfig)
	case y.MaxDelay <= 0:
		return fmt.Errorf("%w: youtube max delay %s", ErrInvalidConfig, y.MaxDelay)
	}
	return nil
}

func (y YouTube) feedURL() string {
	return "https://www.youtube.com/feeds/videos.xml?channel_id=" + y.ChannelID
}

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Entries []atomEntry `xml:"http://www.w3.org/2005/Atom entry"`
}

type atomEntry struct {
	VideoID   string `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
	Title     string `xml:"http://www.w3.org/2005/Atom title"`
	Published string `xml:"http://www.w3.org/2005/Atom published"`
}

type video struct {
	id        string
	title     string
	published time.Time
}

func (v video) url() string {
	return "https://www.youtube.com/watch?v=" + v.id
}

func (a *Adapter) videos(ctx context.Context, window sources.Window) ([]video, error) {
	y := a.cfg.YouTube
	resp, err := a.fetcher.Fetch(ctx, fetch.Request{URL: y.feedURL()})
	if err != nil {
		return nil, fmt.Errorf("vctc youtube feed: %w", err)
	}
	var f atomFeed
	if err := xml.Unmarshal(resp.Body, &f); err != nil {
		return nil, fmt.Errorf("%w: youtube feed: %w", ErrMalformed, err)
	}
	prefix := strings.ToLower(normalize(y.TitlePrefix))
	var out []video
	good, bad := 0, 0
	for _, e := range f.Entries {
		titleText := normalize(e.Title)
		if !strings.HasPrefix(strings.ToLower(titleText), prefix) {
			continue
		}
		v, err := readVideo(e, titleText)
		inside := v.published.IsZero() || (!v.published.Before(window.From) && !v.published.After(window.To.Add(y.MaxDelay)))
		switch {
		case err != nil && inside:
			bad++
			a.logger.WarnContext(ctx, "vctc: skipped an unreadable meeting video", "title", titleText, "error", err)
		case err != nil:
			a.logger.DebugContext(ctx, "vctc: skipped an unreadable meeting video outside the window", "title", titleText, "error", err)
		default:
			if inside {
				good++
			}
			out = append(out, v)
		}
	}
	if good == 0 && bad > 0 {
		return nil, fmt.Errorf("%w: youtube feed: all %d meeting videos in the window are unreadable", ErrMalformed, bad)
	}
	return out, nil
}

func readVideo(e atomEntry, titleText string) (video, error) {
	v := video{id: strings.TrimSpace(e.VideoID), title: titleText}
	published, perr := time.Parse(time.RFC3339, strings.TrimSpace(e.Published))
	if perr == nil {
		v.published = published.UTC()
	}
	var errs []error
	if !videoIDPattern.MatchString(v.id) {
		errs = append(errs, fmt.Errorf("video id %q", e.VideoID))
	}
	if perr != nil {
		errs = append(errs, fmt.Errorf("published time %q", e.Published))
	}
	return v, errors.Join(errs...)
}

func (a *Adapter) matchRecordings(ctx context.Context, entries []entry, videos []video, now time.Time) {
	delay := a.cfg.YouTube.MaxDelay
	matched := make(map[int][]video)
	for _, v := range videos {
		var candidates []int
		for i, e := range entries {
			if !e.cancelled && !e.startsAt.After(v.published) && v.published.Before(e.startsAt.Add(delay)) {
				candidates = append(candidates, i)
			}
		}
		switch len(candidates) {
		case 0:
			a.logger.DebugContext(ctx, "vctc: no board meeting started shortly before this video was published", "video", v.id, "title", v.title)
		case 1:
			matched[candidates[0]] = append(matched[candidates[0]], v)
		default:
			ids := make([]string, len(candidates))
			for i, c := range candidates {
				ids[i] = entries[c].sourceID
			}
			a.logger.WarnContext(ctx, "vctc: a video could belong to several board meetings; none gets it", "video", v.id, "title", v.title, "source_ids", strings.Join(ids, ","))
		}
	}
	for i, vs := range matched {
		e := &entries[i]
		if len(vs) > 1 {
			ids := make([]string, len(vs))
			for j, v := range vs {
				ids[j] = v.id
			}
			a.logger.WarnContext(ctx, "vctc: several videos match one board meeting; none is used", "source_id", e.sourceID, "videos", strings.Join(ids, ","))
			continue
		}
		v := vs[0]
		if e.startsAt.After(now) {
			continue
		}
		local := e.startsAt.In(a.loc)
		if t := parseVideoTitle(v.title, a.cfg.YouTube.TitlePrefix); t.precision != noDate && !t.agrees(local) {
			a.logger.InfoContext(ctx, "vctc: video title date differs from the meeting date; matched by publish time", "source_id", e.sourceID, "video", v.id, "title", v.title, "meeting_date", local.Format(time.DateOnly))
		}
		e.recordingURL = v.url()
	}
}

func parseVideoTitle(titleText, prefix string) title {
	rest := strings.TrimSpace(titleText[min(len(titleText), len(normalize(prefix))):])
	rest = strings.TrimLeft(rest, "-–—: ")
	d, p := parseTitleDate(rest)
	return title{date: d, precision: p}
}
