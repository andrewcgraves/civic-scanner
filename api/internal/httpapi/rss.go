package httpapi

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	maxFeedTitleBytes = 300
	maxFeedTextBytes  = 8000
	feedLanguage      = "en-us"
	atomNamespace     = "http://www.w3.org/2005/Atom"
	feedTimeZone      = "America/Los_Angeles"
	ellipsis          = "…"
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

func renderFeed(feed store.AgencyFeed, webURL string) ([]byte, error) {
	agencyName := plainLine(feed.Agency.Name, maxFeedTitleBytes)
	agencyPage := webURL + "/agencies/" + url.PathEscape(feed.Agency.Slug)
	doc := rssDocument{
		Version: "2.0",
		Atom:    atomNamespace,
		Channel: rssChannel{
			Title:       agencyName + " meetings",
			Link:        agencyPage,
			Description: htmlParagraphs("Agendas, agenda changes and transcripts of " + agencyName + " meetings."),
			Language:    feedLanguage,
			Self:        atomLink{Href: agencyPage + "/feed.xml", Rel: "self", Type: "application/rss+xml"},
			Items:       make([]rssItem, 0, len(feed.Items)),
		},
	}
	if len(feed.Items) > 0 {
		doc.Channel.LastBuildDate = rfc1123(feed.Items[0].OccurredAt)
	}
	for _, item := range feed.Items {
		doc.Channel.Items = append(doc.Channel.Items, feedItem(item, agencyName, webURL))
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode feed: %w", err)
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func feedItem(item store.FeedItem, agencyName, webURL string) rssItem {
	meeting := plainLine(item.MeetingTitle, maxFeedTitleBytes)
	if meeting == "" {
		meeting = "Meeting"
	}
	startsAt := item.MeetingStartsAt.In(meetingTimeZone)
	day := startsAt.Format("Jan 2, 2006")
	when := startsAt.Format("Monday, January 2, 2006 at 3:04 PM MST")

	var title string
	var paragraphs []string
	switch item.Type {
	case store.EventAgendaPublished:
		title = "Agenda posted: "
		paragraphs = append(paragraphs, fmt.Sprintf("%s posted the agenda for %s, %s.", agencyName, meeting, when))
		paragraphs = append(paragraphs, summaryParagraphs(item.AgendaSummary)...)
	case store.EventAgendaChanged:
		title = "Agenda updated: "
		paragraphs = append(paragraphs, fmt.Sprintf("%s changed the agenda for %s, %s.", agencyName, meeting, when))
		if note := plainText(deref(item.AgendaChange), maxFeedTextBytes); note != "" {
			paragraphs = append(paragraphs, "What changed: "+note)
		}
		paragraphs = append(paragraphs, summaryParagraphs(item.AgendaSummary)...)
	default:
		title = "Transcript ready: "
		paragraphs = append(paragraphs, fmt.Sprintf("A transcript of %s, %s, is ready.", meeting, when))
	}

	return rssItem{
		Title:       title + meeting + " (" + day + ")",
		Link:        webURL + "/meetings/" + item.MeetingID.String(),
		Description: htmlParagraphs(paragraphs...),
		GUID:        rssGUID{IsPermaLink: "false", Value: item.EventID.String()},
		PubDate:     rfc1123(item.OccurredAt),
	}
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
