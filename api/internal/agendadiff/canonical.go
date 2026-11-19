package agendadiff

import (
	"context"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCompareBytes    = 4 << 20
	maxJoinedLineBytes = 4096
	boundaryLines      = 1
	runningRepeats     = 2
	checkEvery         = 1024
)

var (
	reImage         = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLink          = regexp.MustCompile(`\[([^\]]*)\]\(((?:[^()\s]|\([^)]*\))*)(?:\s+"[^"]*")?\)`)
	reAutolink      = regexp.MustCompile(`<((?:https?|mailto):[^>\s]+)>`)
	reStrike        = regexp.MustCompile(`~~[^~]+~~`)
	reEscape        = regexp.MustCompile("\\\\([!-/:-@\\[-`{-~])")
	reLetterO       = regexp.MustCompile(`^o\s+`)
	reBlockquote    = regexp.MustCompile(`^(?:>\s*)+([^\d\s])`)
	reMarkerLine    = regexp.MustCompile(`^(?:(?:Page|PAGE|page)\s+(\d{1,4})(?:\s*(?:of|OF|/)\s*\d{1,4})?|(\d{1,4})\s*(?:of|OF|/)\s*\d{1,4}|[-–—]\s*(\d{1,4})\s*[-–—])$`)
	reBareNumber    = regexp.MustCompile(`^\d{1,4}$`)
	rePageSuffix    = regexp.MustCompile(`(?:\s*[-–—|•·:]\s*(?:Page|PAGE)\s+\d{1,4}(?:\s*(?:of|OF|/)\s*\d{1,4})?|\s+(?:Page|PAGE)\s+\d{1,4}\s*(?:of|OF|/)\s*\d{1,4})$`)
	rePagePrefix    = regexp.MustCompile(`^(?:(?:Page|PAGE)\s+\d{1,4}(?:\s*(?:of|OF|/)\s*\d{1,4})?\s*[-–—|•·:]\s*|(?:Page|PAGE)\s+\d{1,4}\s*(?:of|OF|/)\s*\d{1,4}\s+)`)
	ligatures       = strings.NewReplacer("ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi", "ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st")
	symbolChars     = "$€£¥¢%‰§¶±≤≥≠=<>"
	currencySymbols = "$€£¥"
)

type token struct {
	key  string
	word int
}

type document struct {
	words  []string
	tokens []token
}

type line struct {
	text     string
	keys     []string
	boundary bool
}

type page struct {
	lines     []line
	paginated bool
}

type rawLine struct {
	text     string
	marker   int
	bare     int
	boundary bool
}

func hasMarkdownLinks(text string) bool {
	return reLink.MatchString(text)
}

func cleanLine(l string, linkTargets bool) string {
	l = ligatures.Replace(html.UnescapeString(l))
	l = reImage.ReplaceAllString(l, "")
	l = reStrike.ReplaceAllString(l, "")
	if linkTargets {
		l = reLink.ReplaceAllString(l, "$1 $2")
	} else {
		l = reLink.ReplaceAllString(l, "$1")
	}
	l = reAutolink.ReplaceAllString(l, "$1")
	l = reEscape.ReplaceAllString(l, "$1")
	l = strings.TrimSpace(l)
	l = reBlockquote.ReplaceAllString(l, "$1")
	return reLetterO.ReplaceAllString(l, "")
}

func classify(l string) rawLine {
	if m := reMarkerLine.FindStringSubmatch(l); m != nil {
		n, _ := strconv.Atoi(m[1] + m[2] + m[3])
		return rawLine{marker: n}
	}
	if reBareNumber.MatchString(l) {
		n, _ := strconv.Atoi(l)
		return rawLine{text: l, bare: n, marker: -1}
	}
	stripped := rePagePrefix.ReplaceAllString(rePageSuffix.ReplaceAllString(l, ""), "")
	return rawLine{text: stripped, marker: -1, boundary: stripped != l}
}

func prepare(ctx context.Context, text string, linkTargets bool) (page, error) {
	var raws []rawLine
	for i, l := range strings.Split(text, "\n") {
		if i%checkEvery == 0 {
			if err := ctx.Err(); err != nil {
				return page{}, err
			}
		}
		l = cleanLine(l, linkTargets)
		if l == "" {
			continue
		}
		r := classify(l)
		if r.marker < 0 && r.text == "" {
			r.marker = 0
		}
		raws = append(raws, r)
	}

	acceptBareNumbers(raws)
	var p page
	for i := range raws {
		if raws[i].marker < 0 {
			continue
		}
		p.paginated = true
		for j := max(0, i-boundaryLines); j <= min(len(raws)-1, i+boundaryLines); j++ {
			raws[j].boundary = true
		}
	}

	var (
		parts    []string
		partsLen int
		boundary bool
	)
	flush := func() {
		if len(parts) == 0 {
			return
		}
		joined := strings.Join(parts, "")
		var keys []string
		for _, w := range strings.Fields(joined) {
			keys = append(keys, wordKeys(w)...)
		}
		if len(keys) > 0 {
			p.lines = append(p.lines, line{text: joined, keys: keys, boundary: boundary})
		}
		parts, partsLen, boundary = parts[:0], 0, false
	}
	for i, r := range raws {
		if i%checkEvery == 0 {
			if err := ctx.Err(); err != nil {
				return page{}, err
			}
		}
		if r.marker >= 0 {
			continue
		}
		if len(parts) > 0 && partsLen+len(r.text) <= maxJoinedLineBytes &&
			endsWithWordBreak(parts[len(parts)-1]) && startsLower(r.text) {
			parts = append(parts, r.text)
			partsLen += len(r.text)
			boundary = boundary || r.boundary
			continue
		}
		flush()
		parts = append(parts, r.text)
		partsLen = len(r.text)
		boundary = r.boundary
	}
	flush()
	return p, nil
}

func acceptBareNumbers(raws []rawLine) {
	var numbered []int
	for i, r := range raws {
		if r.marker > 0 || r.bare > 0 {
			numbered = append(numbered, i)
		}
	}
	value := func(i int) int {
		if raws[i].marker > 0 {
			return raws[i].marker
		}
		return raws[i].bare
	}
	var accepted []int
	for k, i := range numbered {
		if raws[i].bare == 0 {
			continue
		}
		n := value(i)
		if (k > 0 && value(numbered[k-1]) == n-1) || (k+1 < len(numbered) && value(numbered[k+1]) == n+1) {
			accepted = append(accepted, i)
		}
	}
	for _, i := range accepted {
		raws[i].marker = raws[i].bare
	}
}

func endsWithWordBreak(s string) bool {
	last, size := utf8.DecodeLastRuneInString(s)
	if !isHyphen(last) {
		return false
	}
	before, _ := utf8.DecodeLastRuneInString(s[:len(s)-size])
	return unicode.IsLetter(before)
}

func startsLower(s string) bool {
	first, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLower(first)
}

func isHyphen(r rune) bool {
	return r == '-' || r == '‐' || r == '‑'
}

func isApostrophe(r rune) bool {
	return r == '\'' || r == '’' || r == 'ʼ'
}

func isAlnum(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isSign(r rune) bool {
	return r == '-' || r == '+' || r == '−'
}

func thousandsSeparator(runes []rune, i int) bool {
	group := 0
	for j := i - 1; j >= 0 && unicode.IsDigit(runes[j]); j-- {
		group++
	}
	first := i-group-1 < 0 || runes[i-group-1] != ','
	if group == 0 || group > 3 || (!first && group != 3) {
		return false
	}
	after := 0
	for j := i + 1; j < len(runes) && unicode.IsDigit(runes[j]); j++ {
		after++
	}
	return after == 3
}

func startsNumber(runes []rune, i int) bool {
	if i >= len(runes) {
		return false
	}
	r := runes[i]
	return unicode.IsDigit(r) || strings.ContainsRune(currencySymbols, r) ||
		(r == '.' && i+1 < len(runes) && unicode.IsDigit(runes[i+1]))
}

func wordKeys(word string) []string {
	runes := []rune(word)
	var (
		keys    []string
		current strings.Builder
	)
	flush := func() {
		if current.Len() > 0 {
			keys = append(keys, current.String())
			current.Reset()
		}
	}
	emit := func(s string) {
		flush()
		keys = append(keys, s)
	}
	for i, r := range runes {
		var prev, next rune
		if i > 0 {
			prev = runes[i-1]
		}
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case isAlnum(r):
			current.WriteRune(unicode.ToLower(r))
		case (isHyphen(r) || isApostrophe(r)) && unicode.IsLetter(prev) && unicode.IsLetter(next):
		case r == ',' && unicode.IsDigit(prev) && unicode.IsDigit(next) && thousandsSeparator(runes, i):
		case r == '.' && unicode.IsDigit(prev) && unicode.IsDigit(next):
			current.WriteRune(r)
		case isSign(r) && !isAlnum(prev) && startsNumber(runes, i+1):
			if r == '−' {
				r = '-'
			}
			emit(string(r))
		case r == '(' && startsNumber(runes, i+1):
			emit("(")
		case r == ')' && (unicode.IsDigit(prev) || prev == '%'):
			emit(")")
		case strings.ContainsRune(symbolChars, r):
			emit(string(r))
		default:
			flush()
		}
	}
	flush()
	return keys
}

func lineKey(l line) string {
	return strings.Join(l.keys, " ")
}

func running(p page) map[string]bool {
	counts := map[string]int{}
	repeated := map[string]bool{}
	for _, l := range p.lines {
		if !l.boundary {
			continue
		}
		key := lineKey(l)
		counts[key]++
		if counts[key] >= runningRepeats {
			repeated[key] = true
		}
	}
	return repeated
}

func furniture(own, other page) map[string]bool {
	mine, theirs := running(own), running(other)
	drop := map[string]bool{}
	for key := range mine {
		if theirs[key] || !other.paginated {
			drop[key] = true
		}
	}
	return drop
}

func build(p page, drop map[string]bool) document {
	var doc document
	for _, l := range p.lines {
		if l.boundary && drop[lineKey(l)] {
			continue
		}
		for _, w := range strings.Fields(l.text) {
			index := len(doc.words)
			doc.words = append(doc.words, w)
			for _, k := range wordKeys(w) {
				doc.tokens = append(doc.tokens, token{key: k, word: index})
			}
		}
	}
	return doc
}

func canonical(ctx context.Context, old, updated string) (document, document, error) {
	linkTargets := hasMarkdownLinks(old) && hasMarkdownLinks(updated)
	a, err := prepare(ctx, old, linkTargets)
	if err != nil {
		return document{}, document{}, err
	}
	b, err := prepare(ctx, updated, linkTargets)
	if err != nil {
		return document{}, document{}, err
	}
	return build(a, furniture(a, b)), build(b, furniture(b, a)), nil
}

func tooLarge(old, updated string) bool {
	return len(old) > MaxCompareBytes || len(updated) > MaxCompareBytes
}

func Equivalent(ctx context.Context, old, updated string) bool {
	if tooLarge(old, updated) {
		return false
	}
	a, b, err := canonical(ctx, old, updated)
	if err != nil || len(a.tokens) != len(b.tokens) {
		return false
	}
	for i := range a.tokens {
		if a.tokens[i].key != b.tokens[i].key {
			return false
		}
	}
	return true
}
