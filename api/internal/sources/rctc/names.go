package rctc

import (
	"regexp"
	"strings"
)

var (
	cancelPrefix     = regexp.MustCompile(`(?i)^[\s*]*cancell?ed\b[\s*:\-–—!.]*`)
	cancelSuffix     = regexp.MustCompile(`(?i)[\s*:\-–—]*\(?\s*\bcancell?ed\b\s*\)?[\s*!.]*$`)
	correctionSuffix = regexp.MustCompile(`(?i)[\s:\-–—]*\(?\s*\b(revised|amended|updated)(\s+agenda)?\s*\)?$`)
	trimSeparators   = regexp.MustCompile(`^[\s*:\-–—]+|[\s*:\-–—]+$`)
	slugChars        = regexp.MustCompile(`[^a-z0-9]+`)
)

type name struct {
	full      string
	cleaned   string
	cancelled bool
}

func parseName(raw string) name {
	n := name{full: normalize(raw)}
	s := n.full
	if loc := cancelPrefix.FindStringIndex(s); loc != nil && loc[1] < len(s) {
		n.cancelled = true
		s = s[loc[1]:]
	}
	for {
		before := s
		if loc := cancelSuffix.FindStringIndex(s); loc != nil && loc[0] > 0 {
			n.cancelled = true
			s = s[:loc[0]]
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

func boardSlug(board string) string {
	return strings.Trim(slugChars.ReplaceAllString(strings.ToLower(board), "-"), "-")
}

func looksLikeBoard(full string) bool {
	lower := strings.ToLower(full)
	return strings.Contains(lower, "commission") && !strings.Contains(lower, "committee")
}
