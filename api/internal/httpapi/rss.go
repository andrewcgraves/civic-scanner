package httpapi

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	maxFeedTitleBytes  = 300
	maxFeedTextBytes   = 4000
	maxFeedBytes       = 1 << 20
	itemIndent         = "    "
	agendaSummaryGrace = time.Duration(0)
	feedLanguage       = "en-us"
	atomNamespace      = "http://www.w3.org/2005/Atom"
	feedTimeZone       = "America/Los_Angeles"
	ellipsis           = "…"
)

var meetingTimeZone = mustLoadLocation(feedTimeZone)

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

type rssDocument struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Atom    string     `xml:"xmlns:atom,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Language      string    `xml:"language"`
	Self          atomLink  `xml:"atom:link"`
	LastBuildDate string    `xml:"lastBuildDate,omitempty"`
	Items         []rssItem `xml:"item"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	Description string  `xml:"description"`
	GUID        rssGUID `xml:"guid"`
	PubDate     string  `xml:"pubDate"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type feedRenderer struct {
	webURL       string
	now          func() time.Time
	summaryGrace time.Duration
	maxBytes     int
}

func (r feedRenderer) render(feed store.AgencyFeed) ([]byte, error) {
	var buf bytes.Buffer
	if err := r.write(&buf, feed); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (r feedRenderer) write(w io.Writer, feed store.AgencyFeed) error {
	maxBytes := r.maxBytes
	if maxBytes == 0 {
		maxBytes = maxFeedBytes
	}
	agencyName := plainLine(feed.Agency.Name, maxFeedTitleBytes)
	agencyPage := r.webURL + "/agencies/" + url.PathEscape(feed.Agency.Slug)
	doc := rssDocument{
		Version: "2.0",
		Atom:    atomNamespace,
		Channel: rssChannel{
			Title:       agencyName + " meetings",
			Link:        agencyPage,
			Description: "Agendas, agenda changes and transcripts of " + agencyName + " meetings.",
			Language:    feedLanguage,
			Self:        atomLink{Href: agencyPage + "/feed.xml", Rel: "self", Type: "application/rss+xml"},
		},
	}

	var items []rssItem
	var occurred []time.Time
	for _, event := range feed.Items {
		if item, ok := r.item(event, agencyName); ok {
			items = append(items, item)
			occurred = append(occurred, event.OccurredAt)
		}
	}
	if len(items) > 0 {
		doc.Channel.LastBuildDate = rfc1123(occurred[0])
	}
	size, err := encodedSize(doc)
	if err != nil {
		return err
	}
	size += len(xml.Header) + 1
	for _, item := range items {
		itemSize, err := encodedSize(item)
		if err != nil {
			return err
		}
		if size += itemSize + len(itemIndent) + 1; size > maxBytes {
			break
		}
		doc.Channel.Items = append(doc.Channel.Items, item)
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("write feed: %w", err)
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode feed: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("write feed: %w", err)
	}
	return nil
}

func encodedSize(v any) (int, error) {
	out, err := xml.MarshalIndent(v, itemIndent, "  ")
	if err != nil {
		return 0, fmt.Errorf("measure feed: %w", err)
	}
	return len(out), nil
}

func (r feedRenderer) item(event store.FeedItem, agencyName string) (rssItem, bool) {
	meeting := plainLine(event.MeetingTitle, maxFeedTitleBytes)
	if meeting == "" {
		meeting = "Meeting"
	}
	startsAt := event.MeetingStartsAt.In(meetingTimeZone)
	day := startsAt.Format("Jan 2, 2006")
	when := startsAt.Format("Monday, January 2, 2006 at 3:04 PM MST")

	var title string
	var paragraphs []string
	switch event.Type {
	case store.EventAgendaPublished:
		if !r.agendaReady(event) {
			return rssItem{}, false
		}
		title = "Agenda posted: "
		paragraphs = append(paragraphs, fmt.Sprintf("%s posted the agenda for %s, %s.", agencyName, meeting, when))
		paragraphs = append(paragraphs, summaryParagraphs(event.AgendaSummary)...)
	case store.EventAgendaChanged:
		if !r.agendaReady(event) {
			return rssItem{}, false
		}
		title = "Agenda updated: "
		paragraphs = append(paragraphs, fmt.Sprintf("%s changed the agenda for %s, %s.", agencyName, meeting, when))
		if note := plainText(deref(event.AgendaChange), maxFeedTextBytes); note != "" {
			paragraphs = append(paragraphs, "What changed: "+note)
		}
		paragraphs = append(paragraphs, summaryParagraphs(event.AgendaSummary)...)
	case store.EventTranscriptReady:
		title = "Transcript ready: "
		paragraphs = append(paragraphs, fmt.Sprintf("A transcript of %s, %s, is ready.", meeting, when))
	default:
		return rssItem{}, false
	}

	return rssItem{
		Title:       title + meeting + " (" + day + ")",
		Link:        r.webURL + "/meetings/" + event.MeetingID.String(),
		Description: htmlParagraphs(paragraphs...),
		GUID:        rssGUID{IsPermaLink: "false", Value: event.EventID.String()},
		PubDate:     rfc1123(event.OccurredAt),
	}, true
}

func (r feedRenderer) agendaReady(event store.FeedItem) bool {
	return r.summaryGrace <= 0 || event.AgendaSummary != nil || r.now().Sub(event.OccurredAt) >= r.summaryGrace
}

func summaryParagraphs(summary *string) []string {
	text := plainText(deref(summary), maxFeedTextBytes)
	if text == "" {
		return []string{"A summary is not available yet."}
	}
	return []string{"Summary: " + text}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func rfc1123(t time.Time) string {
	return t.UTC().Format(time.RFC1123Z)
}

func htmlParagraphs(paragraphs ...string) string {
	var b strings.Builder
	for _, p := range paragraphs {
		for line := range strings.SplitSeq(p, "\n") {
			if line == "" {
				continue
			}
			b.WriteString("<p>")
			b.WriteString(html.EscapeString(line))
			b.WriteString("</p>")
		}
	}
	return b.String()
}

func plainLine(s string, limit int) string {
	return truncate(strings.Join(strings.Fields(strip(s)), " "), limit)
}

func plainText(s string, limit int) string {
	var lines []string
	for line := range strings.SplitSeq(strip(s), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return truncate(strings.Join(lines, "\n"), limit)
}

func strip(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == '\r' {
			r = '\n'
		}
		if keepRune(r, size) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func keepRune(r rune, size int) bool {
	switch {
	case r == utf8.RuneError && size <= 1:
		return false
	case r == '\t' || r == '\n':
		return true
	case unicode.IsControl(r):
		return false
	case r == 0xFFFE, r == 0xFFFF:
		return false
	case unicode.Is(unicode.Cf, r), r >= 0xE0000 && r <= 0xE007F:
		return false
	}
	return true
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit - len(ellipsis)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return strings.TrimSpace(s[:end]) + ellipsis
}
