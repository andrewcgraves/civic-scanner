package vctc

import (
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var months = map[string]time.Month{
	"january": time.January, "jan": time.January,
	"february": time.February, "feb": time.February,
	"march": time.March, "mar": time.March,
	"april": time.April, "apr": time.April,
	"may":  time.May,
	"june": time.June, "jun": time.June,
	"july": time.July, "jul": time.July,
	"august": time.August, "aug": time.August,
	"september": time.September, "sept": time.September, "sep": time.September,
	"october": time.October, "oct": time.October,
	"november": time.November, "nov": time.November,
	"december": time.December, "dec": time.December,
}

const monthNames = `january|february|march|april|may|june|july|august|september|october|november|december|sept|jan|feb|mar|apr|jun|jul|aug|sep|oct|nov|dec`

var (
	titleMonth = regexp.MustCompile(`(?i)^(` + monthNames + `)\.?\s+(\d{4})$`)
	titleDay   = regexp.MustCompile(`(?i)^(` + monthNames + `)\.?\s+(\d{1,2}),?\s+(\d{4})$`)
	nameDay    = regexp.MustCompile(`(?i)(?:^|[^a-z])(` + monthNames + `)\.?[-_ ]+(\d{1,2})(?:st|nd|rd|th)?[-_ ,]+(\d{4})(?:[^0-9]|$)`)
	isoDay     = regexp.MustCompile(`(?:^|[^0-9])(\d{4})[._-](\d{1,2})[._-](\d{1,2})(?:[^0-9]|$)`)
	dottedDay  = regexp.MustCompile(`(?:^|[^0-9.])(\d{1,2})\.(\d{1,2})\.(\d{4}|\d{2})(?:[^0-9]|$)`)
	dashedDay  = regexp.MustCompile(`(?:^|[^0-9])(\d{1,2})-(\d{1,2})-(\d{4})(?:[^0-9]|$)`)
)

func parseTitleDate(s string) (time.Time, datePrecision) {
	s = normalize(s)
	if m := titleDay.FindStringSubmatch(s); m != nil {
		if d, ok := civil(m[3], months[strings.ToLower(m[1])], m[2]); ok {
			return d, dayDate
		}
	}
	if m := titleMonth.FindStringSubmatch(s); m != nil {
		if d, ok := civil(m[2], months[strings.ToLower(m[1])], "1"); ok {
			return d, monthDate
		}
	}
	return time.Time{}, noDate
}

func civil(year string, month time.Month, day string) (time.Time, bool) {
	y, _ := strconv.Atoi(year)
	if y < 100 {
		y += 2000
	}
	d, _ := strconv.Atoi(day)
	if month < time.January || month > time.December || d < 1 {
		return time.Time{}, false
	}
	t := time.Date(y, month, d, 0, 0, 0, 0, time.UTC)
	return t, t.Day() == d
}

func numericMonth(s string) time.Month {
	n, _ := strconv.Atoi(s)
	return time.Month(n)
}

func linkDates(l link) []time.Time {
	name := l.href
	if u, err := url.Parse(l.href); err == nil {
		name = path.Base(u.Path)
	}
	var out []time.Time
	add := func(d time.Time, ok bool) {
		if ok && !containsDay(out, d) {
			out = append(out, d)
		}
	}
	for _, s := range []string{name, l.text} {
		for _, m := range nameDay.FindAllStringSubmatch(s, -1) {
			add(civil(m[3], months[strings.ToLower(m[1])], m[2]))
		}
		for _, m := range isoDay.FindAllStringSubmatch(s, -1) {
			add(civil(m[1], numericMonth(m[2]), m[3]))
		}
		for _, m := range dottedDay.FindAllStringSubmatch(s, -1) {
			add(civil(m[3], numericMonth(m[1]), m[2]))
		}
		for _, m := range dashedDay.FindAllStringSubmatch(s, -1) {
			add(civil(m[3], numericMonth(m[1]), m[2]))
		}
	}
	return out
}

func containsDay(days []time.Time, d time.Time) bool {
	for _, x := range days {
		if x.Equal(d) {
			return true
		}
	}
	return false
}
