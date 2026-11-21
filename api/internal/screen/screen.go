package screen

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
)

type Verdict string

const (
	VerdictBoth           Verdict = "both"
	VerdictAgendasOnly    Verdict = "agendas_only"
	VerdictRecordingsOnly Verdict = "recordings_only"
	VerdictNeither        Verdict = "neither"
)

type Supports string

const (
	SupportsAgendas    Supports = "agendas"
	SupportsRecordings Supports = "recordings"
)

const (
	DefaultMaxPages    = 6
	DefaultMaxBytes    = 8 << 20
	MaxEvidencePerKind = 10
)

var ErrNoFetcher = errors.New("screen: no fetcher configured")

type Evidence struct {
	Supports Supports
	Signal   string
	URL      string
	Page     string
}

type Page struct {
	URL   string
	Error string
}

type Report struct {
	Verdict  Verdict
	Evidence []Evidence
	Pages    []Page
}

type Screener struct {
	Fetcher  fetch.Fetcher
	MaxPages int
	MaxBytes int64
}

func Retryable(err error) bool {
	return fetch.IsRetryable(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s Screener) Screen(ctx context.Context, website string) (Report, error) {
	if s.Fetcher == nil {
		return Report{}, ErrNoFetcher
	}
	maxPages := s.MaxPages
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	home, err := s.fetch(ctx, website)
	if err != nil {
		return Report{}, fmt.Errorf("home page %s: %w", website, err)
	}
	base, err := url.Parse(home.FinalURL)
	if err != nil {
		return Report{}, fmt.Errorf("home page %s: final url %q: %w", website, home.FinalURL, err)
	}
	c := crawl{
		sites:   siteNames(website, base.Hostname()),
		seen:    map[string]bool{pageKey(website): true, pageKey(home.FinalURL): true},
		found:   map[string]bool{},
		counts:  map[Supports]int{},
		fetched: 1,
	}
	c.report.Pages = append(c.report.Pages, Page{URL: home.FinalURL})
	c.visit(base, home.Body, true)

	for c.fetched < maxPages && len(c.queue) > 0 {
		next := c.pop()
		resp, err := s.fetch(ctx, next.url.String())
		if err != nil {
			if Retryable(err) {
				return Report{}, fmt.Errorf("page %s: %w", next.url, err)
			}
			c.report.Pages = append(c.report.Pages, Page{URL: next.url.String(), Error: err.Error()})
			continue
		}
		c.fetched++
		final, err := url.Parse(resp.FinalURL)
		if err != nil {
			final = next.url
		}
		c.seen[pageKey(final.String())] = true
		c.report.Pages = append(c.report.Pages, Page{URL: final.String()})
		c.visit(final, resp.Body, false)
	}
	c.report.Verdict = verdict(c.counts)
	return c.report, nil
}

func (s Screener) fetch(ctx context.Context, target string) (fetch.Response, error) {
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return s.Fetcher.Fetch(ctx, fetch.Request{URL: target, MaxBytes: maxBytes, RawHTML: true})
}

func verdict(counts map[Supports]int) Verdict {
	agendas, recordings := counts[SupportsAgendas] > 0, counts[SupportsRecordings] > 0
	switch {
	case agendas && recordings:
		return VerdictBoth
	case agendas:
		return VerdictAgendasOnly
	case recordings:
		return VerdictRecordingsOnly
	}
	return VerdictNeither
}

type candidate struct {
	url      *url.URL
	priority int
	order    int
}

type crawl struct {
	sites   []string
	seen    map[string]bool
	found   map[string]bool
	counts  map[Supports]int
	queue   []candidate
	order   int
	fetched int
	report  Report
}

func (c *crawl) visit(page *url.URL, body []byte, home bool) {
	for _, l := range extractLinks(page, body) {
		for _, e := range classify(l, home) {
			c.addEvidence(Evidence{Supports: e.supports, Signal: e.signal, URL: l.url.String(), Page: page.String()})
		}
		c.consider(l)
	}
}

func (c *crawl) addEvidence(e Evidence) {
	key := string(e.Supports) + " " + e.URL
	if c.found[key] || c.counts[e.Supports] >= MaxEvidencePerKind {
		return
	}
	c.found[key] = true
	c.counts[e.Supports]++
	c.report.Evidence = append(c.report.Evidence, e)
}

func (c *crawl) consider(l link) {
	if l.embedded || !pageLike(l.url) {
		return
	}
	priority := c.priority(l)
	if priority == 0 {
		return
	}
	key := pageKey(l.url.String())
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.order++
	c.queue = append(c.queue, candidate{url: l.url, priority: priority, order: c.order})
}

func (c *crawl) priority(l link) int {
	host := strings.ToLower(l.url.Hostname())
	if meetingPlatform(host) {
		return 3
	}
	if !c.sameSite(host) {
		return 0
	}
	words := strings.ToLower(l.text + " " + l.url.Path)
	if containsAny(words, primaryWords) {
		return 2
	}
	if containsAny(words, secondaryWords) {
		return 1
	}
	return 0
}

func (c *crawl) sameSite(host string) bool {
	for _, site := range c.sites {
		if host == site || strings.HasSuffix(host, "."+site) {
			return true
		}
	}
	return false
}

func (c *crawl) pop() candidate {
	best := 0
	for i, cand := range c.queue {
		top := c.queue[best]
		if cand.priority > top.priority || (cand.priority == top.priority && cand.order < top.order) {
			best = i
		}
	}
	next := c.queue[best]
	c.queue = slices.Delete(c.queue, best, best+1)
	return next
}

func siteNames(website, finalHost string) []string {
	var sites []string
	for _, host := range []string{hostOf(website), finalHost} {
		site := strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(host), "."), "www.")
		if site != "" && !slices.Contains(sites, site) {
			sites = append(sites, site)
		}
	}
	return sites
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func pageKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	u.Host = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.Scheme = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

var documentSuffixes = []string{
	".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".zip", ".csv", ".txt",
	".jpg", ".jpeg", ".png", ".gif", ".svg", ".webp", ".ics",
	".mp3", ".mp4", ".m4a", ".m3u8", ".mov", ".wav",
}

func pageLike(u *url.URL) bool {
	path := strings.ToLower(u.Path)
	for _, suffix := range documentSuffixes {
		if strings.HasSuffix(path, suffix) {
			return false
		}
	}
	return !videoHost(strings.ToLower(u.Hostname()))
}

var (
	primaryWords   = []string{"agenda", "meeting"}
	secondaryWords = []string{"board", "commission", "committee", "council", "minutes", "video", "recording", "webcast", "livestream", "hearing"}
	recordingWords = []string{"meeting", "board", "commission", "committee", "council", "recording", "webcast", "livestream", "live stream", "archive"}
)

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
