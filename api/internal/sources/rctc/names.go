package rctc

import (
	"regexp"
	"slices"
	"strings"
)

const statusWords = `cancell?ed|postponed|rescheduled`

var (
	statusPrefix     = regexp.MustCompile(`(?i)^[\s*]*(` + statusWords + `)\b[\s*:\-–—!.]*`)
	statusSuffix     = regexp.MustCompile(`(?i)[\s*:\-–—]*\(?\s*\b(` + statusWords + `)\b\s*\)?[\s*!.]*$`)
	correctionSuffix = regexp.MustCompile(`(?i)[\s:\-–—]*\(?\s*\b(revised|amended|updated)(\s+agenda)?\s*\)?$`)
	trimSeparators   = regexp.MustCompile(`^[\s*:\-–—]+|[\s*:\-–—]+$`)
	slugChars        = regexp.MustCompile(`[^a-z0-9]+`)
	standaloneStatus = regexp.MustCompile(`(?i)^[\s*:\-–—()!.]*(` + statusWords + `)[\s*:\-–—()!.]*$`)
)

type marker int

const (
	noMarker marker = iota
	cancelMarker
	rescheduleMarker
)

func markerOf(word string) marker {
	if strings.EqualFold(word, "rescheduled") {
		return rescheduleMarker
	}
	return cancelMarker
}

func (m marker) merge(other marker) marker {
	if m == cancelMarker || other == cancelMarker {
		return cancelMarker
	}
	return max(m, other)
}

type name struct {
	full    string
	cleaned string
	marker  marker
}

func parseName(raw string) name {
	n := name{full: normalize(raw)}
	s := n.full
	if m := statusPrefix.FindStringSubmatchIndex(s); m != nil && m[1] < len(s) {
		n.marker = n.marker.merge(markerOf(s[m[2]:m[3]]))
		s = s[m[1]:]
	}
	for {
		before := s
		if m := statusSuffix.FindStringSubmatchIndex(s); m != nil && m[0] > 0 {
			n.marker = n.marker.merge(markerOf(s[m[2]:m[3]]))
			s = s[:m[0]]
		}
		if loc := correctionSuffix.FindStringIndex(s); loc != nil && loc[0] > 0 {
			s = s[:loc[0]]
		}
		s = trimSeparators.ReplaceAllString(s, "")
		if s == before {
			break
		}
	}
	n.cleaned = normalize(s)
	return n
}

func standaloneMarker(text string) marker {
	if m := standaloneStatus.FindStringSubmatch(text); m != nil {
		return markerOf(m[1])
	}
	return noMarker
}

func boardSlug(board string) string {
	return strings.Trim(slugChars.ReplaceAllString(strings.ToLower(board), "-"), "-")
}

func looksLikeBoard(full string) bool {
	lower := strings.ToLower(full)
	return strings.Contains(lower, "commission") && !strings.Contains(lower, "committee")
}

var supportingWords = []string{
	"report", "reports", "presentation", "presentations", "video", "videos", "minutes",
	"attachment", "attachments", "exhibit", "exhibits", "item", "items", "audio", "recording",
	"recordings", "slides", "handout", "handouts", "comments", "correspondence", "memo", "stream", "livestream",
}

func supporting(words []string) bool {
	return slices.ContainsFunc(words, func(w string) bool { return slices.Contains(supportingWords, w) })
}

func revisedWord(words []string) bool {
	return slices.Contains(words, "revised") || slices.Contains(words, "amended") || slices.Contains(words, "updated")
}
