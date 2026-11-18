package vctc

import (
	"html"
	"regexp"
	"strings"
	"time"
)

type title struct {
	full      string
	name      string
	whole     string
	cancelled bool
	date      time.Time
	precision datePrecision
}

type datePrecision int

const (
	noDate datePrecision = iota
	monthDate
	dayDate
)

var (
	cancelPrefix   = regexp.MustCompile(`(?i)^[\s*]*cancell?ed\b[\s*:\-–—]*`)
	cancelSuffix   = regexp.MustCompile(`(?i)\s+[-–—]\s+cancell?ed$`)
	trailingParens = regexp.MustCompile(`\s*\(([^()]*)\)$`)
	cancelWord     = regexp.MustCompile(`(?i)^cancell?ed$`)
	separator      = regexp.MustCompile(`\s+[-–—]\s+`)
)

var (
	monthLayouts = []string{"January 2006", "Jan 2006", "Jan. 2006"}
	dayLayouts   = []string{"January 2, 2006", "Jan 2, 2006", "Jan. 2, 2006", "January 2 2006"}
)

func parseTitle(raw string) title {
	full := normalize(html.UnescapeString(raw))
	t := title{full: full}
	s := full
	if loc := cancelPrefix.FindStringIndex(s); loc != nil && loc[1] < len(s) {
		t.cancelled = true
		s = s[loc[1]:]
	}
	for {
		before := s
		if m := trailingParens.FindStringSubmatchIndex(s); m != nil && m[0] > 0 {
			if cancelWord.MatchString(strings.TrimSpace(s[m[2]:m[3]])) {
				t.cancelled = true
			}
			s = strings.TrimSpace(s[:m[0]])
		}
		if loc := cancelSuffix.FindStringIndex(s); loc != nil && loc[0] > 0 {
			t.cancelled = true
			s = strings.TrimSpace(s[:loc[0]])
		}
		if s == before {
			break
		}
	}
	t.whole = s
	t.name = s
	if locs := separator.FindAllStringIndex(s, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		t.name = s[:last[0]]
		t.date, t.precision = parseTitleDate(s[last[1]:])
	}
	return t
}

func parseTitleDate(s string) (time.Time, datePrecision) {
	s = normalize(s)
	for _, layout := range dayLayouts {
		if d, err := time.Parse(layout, s); err == nil {
			return d, dayDate
		}
	}
	for _, layout := range monthLayouts {
		if d, err := time.Parse(layout, s); err == nil {
			return d, monthDate
		}
	}
	return time.Time{}, noDate
}

func (t title) agrees(day time.Time) bool {
	switch t.precision {
	case dayDate:
		return t.date.Year() == day.Year() && t.date.Month() == day.Month() && t.date.Day() == day.Day()
	case monthDate:
		return t.date.Year() == day.Year() && t.date.Month() == day.Month()
	default:
		return true
	}
}

func (t title) overlaps(window windowDays) bool {
	if t.precision == noDate {
		return true
	}
	first := t.date
	last := t.date
	if t.precision == monthDate {
		last = first.AddDate(0, 1, -1)
	}
	return !last.Before(window.first) && !first.After(window.last)
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
