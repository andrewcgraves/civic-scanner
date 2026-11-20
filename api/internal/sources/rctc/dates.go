package rctc

import (
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	namedDate   = regexp.MustCompile(`(?i)\b(` + monthNames + `)\.?\s+(\d{1,2})(?:st|nd|rd|th)?\s*,?\s*(\d{4})\b`)
	slashedDate = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`)
	clock       = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*([ap])\.?\s*m\b\.?`)
	noon        = regexp.MustCompile(`(?i)\bnoon\b`)
	weekday     = regexp.MustCompile(`(?i)\b(monday|tuesday|wednesday|thursday|friday|saturday|sunday|mon|tue|tues|wed|thu|thur|thurs|fri|sat|sun)\b\.?`)
	slug        = regexp.MustCompile(`^(` + monthNames + `)(\d{4})([a-z]+)$`)
	movedFrom   = regexp.MustCompile(`(?i)[(\[]\s*(?:rescheduled|moved|postponed|changed)\s+from\b[^)\]]*[)\]]`)
	statusWord  = regexp.MustCompile(`(?i)\b(cancell?ed|postponed|rescheduled)\b`)
)

type day struct {
	year  int
	month time.Month
	day   int
}

func (d day) String() string {
	return time.Date(d.year, d.month, d.day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
}

type when struct {
	date      day
	hasDate   bool
	dates     int
	hour      int
	minute    int
	hasTime   bool
	timeText  bool
	called    bool
	movedFrom bool
}

func civil(year int, month time.Month, d int) (day, bool) {
	if month < time.January || month > time.December || d < 1 || year < 1900 || year > 2999 {
		return day{}, false
	}
	t := time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
	return day{year: year, month: month, day: d}, t.Day() == d && t.Month() == month
}

func parseWhen(text string) when {
	var w when
	if movedFrom.MatchString(text) {
		w.movedFrom = true
		text = movedFrom.ReplaceAllString(text, " ")
	}
	rest := text
	var found []day
	add := func(d day, ok bool) {
		if !ok {
			return
		}
		for _, f := range found {
			if f == d {
				return
			}
		}
		found = append(found, d)
	}
	for _, m := range namedDate.FindAllStringSubmatch(text, -1) {
		y, _ := strconv.Atoi(m[3])
		d, _ := strconv.Atoi(m[2])
		add(civil(y, months[strings.ToLower(m[1])], d))
	}
	for _, m := range slashedDate.FindAllStringSubmatch(text, -1) {
		y, _ := strconv.Atoi(m[3])
		mo, _ := strconv.Atoi(m[1])
		d, _ := strconv.Atoi(m[2])
		add(civil(y, time.Month(mo), d))
	}
	rest = namedDate.ReplaceAllString(rest, " ")
	rest = slashedDate.ReplaceAllString(rest, " ")
	rest = weekday.ReplaceAllString(rest, " ")
	if statusWord.MatchString(rest) {
		w.called = true
		rest = statusWord.ReplaceAllString(rest, " ")
	}
	w.dates = len(found)
	if len(found) == 1 {
		w.date, w.hasDate = found[0], true
	}

	w.timeText = strings.IndexFunc(rest, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
	if m := clock.FindStringSubmatch(rest); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi := 0
		if m[2] != "" {
			mi, _ = strconv.Atoi(m[2])
		}
		if h >= 1 && h <= 12 && mi >= 0 && mi <= 59 {
			if strings.EqualFold(m[3], "p") && h != 12 {
				h += 12
			}
			if strings.EqualFold(m[3], "a") && h == 12 {
				h = 0
			}
			w.hour, w.minute, w.hasTime = h, mi, true
		}
	} else if noon.MatchString(rest) {
		w.hour, w.minute, w.hasTime = 12, 0, true
	}
	return w
}

type slugMonth struct {
	year  int
	month time.Month
	kind  string
}

func linkSlug(href string) (slugMonth, bool) {
	u, err := url.Parse(href)
	if err != nil {
		return slugMonth{}, false
	}
	name := strings.ToLower(strings.Trim(path.Base(strings.TrimSuffix(u.Path, "/")), "/"))
	m := slug.FindStringSubmatch(name)
	if m == nil {
		return slugMonth{}, false
	}
	y, _ := strconv.Atoi(m[2])
	return slugMonth{year: y, month: months[m[1]], kind: m[3]}, true
}

func linkWords(href string) []string {
	name := href
	if u, err := url.Parse(href); err == nil {
		name = path.Base(u.Path)
	}
	return wordsOf(name)
}

func wordsOf(name string) []string {
	var words []string
	var b strings.Builder
	var last rune
	flush := func() {
		if b.Len() > 0 {
			words = append(words, b.String())
			b.Reset()
		}
	}
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z':
			if last >= '0' && last <= '9' {
				flush()
			}
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if last >= 'a' && last <= 'z' {
				flush()
			}
			b.WriteRune(r)
		default:
			flush()
		}
		last = r
	}
	flush()
	return words
}
