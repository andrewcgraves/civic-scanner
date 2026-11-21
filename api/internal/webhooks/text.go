package webhooks

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	ellipsis      = "…"
	ellipsisUnits = 1
	maxLinkBytes  = 2048
)

func textUnits(s string) int {
	n := 0
	for _, r := range s {
		n += max(utf16.RuneLen(r), 1)
	}
	return n
}

func plain(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return -1
		case unicode.IsControl(r) || r == unicode.ReplacementChar:
			return -1
		default:
			return r
		}
	}, strings.TrimSpace(s))
}

func fit(s string, limit int, escape func(rune) string) (string, bool) {
	s = plain(s)
	if escape == nil {
		escape = func(r rune) string { return string(r) }
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		used += textUnits(escape(r))
	}
	if used <= limit {
		for _, r := range s {
			b.WriteString(escape(r))
		}
		return b.String(), false
	}
	if limit < ellipsisUnits {
		return "", true
	}
	used = 0
	for _, r := range s {
		piece := escape(r)
		n := textUnits(piece)
		if used+n > limit-ellipsisUnits {
			break
		}
		b.WriteString(piece)
		used += n
	}
	return strings.TrimRightFunc(b.String(), unicode.IsSpace) + ellipsis, true
}

func MeetingPage(webURL string, m store.Meeting) string {
	if base := strings.TrimRight(webURL, "/"); base != "" {
		if link := base + "/meetings/" + m.ID.String(); usableLink(link) {
			return link
		}
	}
	if usableLink(m.SourceURL) {
		return m.SourceURL
	}
	return ""
}

func usableLink(raw string) bool {
	if raw == "" || len(raw) > maxLinkBytes || strings.ContainsAny(raw, " \t\r\n<>|") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}
