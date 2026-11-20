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
	special   bool
}

var (
	cancelPrefix  = regexp.MustCompile(`(?i)^[\s*]*cancell?ed\b[\s*:\-–—]*`)
	markerSuffix  = regexp.MustCompile(`(?i)[\s(\[*:\-–—]*\b(cancell?ed|dark)\b[\s)\]*!.:\-–—]*$`)
	specialPrefix = regexp.MustCompile(`(?i)^special\s+(meeting\s+of\s+(the\s+)?)?`)
	specialSuffix = regexp.MustCompile(`(?i)[\s(\[:\-–—]*\bspecial(\s+meeting)?\b[\s)\]]*$`)
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
		m := markerSuffix.FindStringSubmatchIndex(s)
		if m == nil || m[0] == 0 {
			break
		}
		if strings.EqualFold(s[m[2]:m[3]], "dark") {
			t.dark = true
		} else {
			t.cancelled = true
		}
		s = strings.TrimSpace(s[:m[0]])
	}
	t.display = s
	name := s
	if loc := specialPrefix.FindStringIndex(name); loc != nil && loc[1] < len(name) {
		t.special = true
		name = name[loc[1]:]
	}
	if loc := specialSuffix.FindStringIndex(name); loc != nil && loc[0] > 0 {
		t.special = true
		name = strings.TrimSpace(name[:loc[0]])
	}
	t.name = name
	return t
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
