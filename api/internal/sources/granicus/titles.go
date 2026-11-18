package granicus

import (
	"regexp"
	"strings"
	"time"
)

type title struct {
	name      string
	date      time.Time
	hasDate   bool
	cancelled bool
}

var (
	dateSuffix      = regexp.MustCompile(`\s*-\s*([A-Za-z]{3,9})\.? (\d{1,2}), (\d{4})$`)
	cancelPrefix    = regexp.MustCompile(`(?i)^[\s*(\-]*cancell?ed\b[\s:)\-]*`)
	cancelSuffix    = regexp.MustCompile(`(?i)[\s(\-]*\bcancell?ed\b[\s)!.\-]*$`)
	correctionMark  = regexp.MustCompile(`(?i)\s*-\s*(corrected|revised|amended|updated)( agenda)?[\s\-]*$`)
	trailingParens  = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
	leadingAsterisk = regexp.MustCompile(`^[\s*]+`)
	nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)
)

func parseTitle(raw string) title {
	var t title
	name := normalize(raw)
	if m := dateSuffix.FindStringSubmatch(name); m != nil {
		for _, layout := range []string{"Jan 2 2006", "January 2 2006"} {
			if d, err := time.Parse(layout, m[1]+" "+m[2]+" "+m[3]); err == nil {
				t.date, t.hasDate = d, true
				name = name[:len(name)-len(m[0])]
				break
			}
		}
	}
	name = leadingAsterisk.ReplaceAllString(name, "")
	if loc := cancelPrefix.FindStringIndex(name); loc != nil {
		t.cancelled = true
		name = name[loc[1]:]
	}
	for {
		before := name
		if loc := cancelSuffix.FindStringIndex(name); loc != nil && loc[0] > 0 {
			t.cancelled = true
			name = name[:loc[0]]
		}
		name = correctionMark.ReplaceAllString(name, "")
		name = trailingParens.ReplaceAllString(name, "")
		name = strings.TrimSpace(name)
		if name == before {
			break
		}
	}
	t.name = name
	return t
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func slug(name string) string {
	return strings.Trim(nonAlphanumeric.ReplaceAllString(strings.ToLower(name), "-"), "-")
}
