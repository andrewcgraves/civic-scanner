package primegov

import (
	"html"
	"regexp"
	"strings"
)

type title struct {
	full      string
	display   string
	name      string
	cancelled bool
	dark      bool
	postponed bool
	test      bool
}

var (
	markerPrefix    = regexp.MustCompile(`(?i)^[\s*]*(cancell?ed|postponed|rescheduled)\b[\s*:\-–—]*`)
	markerSuffix    = regexp.MustCompile(`(?i)[\s(\[*:\-–—]*\b(cancell?ed|dark|postponed|rescheduled)\b[\s)\]*!.:\-–—]*$`)
	qualifierPrefix = regexp.MustCompile(`(?i)^(special|regular)\s+(meeting\s+of\s+(the\s+)?)?`)
	meetingSuffix   = regexp.MustCompile(`(?i)[\s(\[:\-–—]*\b((special|regular)\s+)?meeting\b[\s)\]]*$`)
	qualifierSuffix = regexp.MustCompile(`(?i)[\s(\[:\-–—]*\b(special|regular)\b[\s)\]]*$`)
	testEntry       = regexp.MustCompile(`(?i)\btest\b|\bplease\s+ignore\b`)
)

func parseTitle(raw string) title {
	full := normalize(html.UnescapeString(raw))
	t := title{full: full, test: testEntry.MatchString(full)}
	s := full
	if m := markerPrefix.FindStringSubmatchIndex(s); m != nil && m[1] < len(s) {
		t.mark(s[m[2]:m[3]])
		s = s[m[1]:]
	}
	for {
		m := markerSuffix.FindStringSubmatchIndex(s)
		if m == nil || m[0] == 0 {
			break
		}
		t.mark(s[m[2]:m[3]])
		s = strings.TrimSpace(s[:m[0]])
	}
	t.display = s
	t.name = stripQualifiers(s)
	return t
}

func (t *title) mark(word string) {
	switch strings.ToLower(word) {
	case "dark":
		t.dark = true
	case "postponed":
		t.postponed = true
	case "rescheduled":
	default:
		t.cancelled = true
	}
}

func stripQualifiers(s string) string {
	for {
		before := s
		if loc := qualifierPrefix.FindStringIndex(s); loc != nil && loc[1] < len(s) {
			s = s[loc[1]:]
		}
		for _, suffix := range []*regexp.Regexp{meetingSuffix, qualifierSuffix} {
			if loc := suffix.FindStringIndex(s); loc != nil && loc[0] > 0 {
				s = strings.TrimSpace(s[:loc[0]])
			}
		}
		if s == before {
			return s
		}
	}
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
