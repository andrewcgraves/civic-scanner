package agendadiff

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const boilerplateRepeats = 2

var (
	reImage      = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLink       = regexp.MustCompile(`\[([^\]]*)\]\((?:[^()\s]|\([^)]*\))*(?:\s+"[^"]*")?\)`)
	reAutolink   = regexp.MustCompile(`<((?:https?|mailto):[^>\s]+)>`)
	reEscape     = regexp.MustCompile("\\\\([!-/:-@\\[-`{-~])")
	reLetterO    = regexp.MustCompile(`^o\s+`)
	rePagePhrase = regexp.MustCompile(`(?i)\bpage\s+\d{1,4}(?:\s*(?:of|/)\s*\d{1,4})?\b`)
	rePageLine   = regexp.MustCompile(`^[-–—]?\s*\d{1,4}(?:\s*(?:of|/)\s*\d{1,4})?\s*[-–—]?$`)
	ligatures    = strings.NewReplacer("ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi", "ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st")
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
	text string
	keys []string
}

func prepare(text string) []line {
	raw := strings.Split(ligatures.Replace(html.UnescapeString(text)), "\n")
	cleaned := make([]string, 0, len(raw))
	for _, l := range raw {
		l = reImage.ReplaceAllString(l, "")
		l = reLink.ReplaceAllString(l, "$1")
		l = reAutolink.ReplaceAllString(l, "$1")
		l = reEscape.ReplaceAllString(l, "$1")
		l = rePagePhrase.ReplaceAllString(l, "")
		l = strings.TrimSpace(l)
		l = reLetterO.ReplaceAllString(l, "")
		if l == "" || rePageLine.MatchString(l) {
			continue
		}
		if n := len(cleaned); n > 0 && endsWithWordBreak(cleaned[n-1]) && startsLower(l) {
			cleaned[n-1] += l
			continue
		}
		cleaned = append(cleaned, l)
	}
	lines := make([]line, 0, len(cleaned))
	for _, l := range cleaned {
		var keys []string
		for _, w := range strings.Fields(l) {
			keys = append(keys, wordKeys(w)...)
		}
		if len(keys) > 0 {
			lines = append(lines, line{text: l, keys: keys})
		}
	}
	return lines
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
	for i, r := range runes {
		var prev, next rune
		if i > 0 {
			prev = runes[i-1]
		}
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			current.WriteRune(unicode.ToLower(r))
		case (isHyphen(r) || isApostrophe(r)) && unicode.IsLetter(prev) && unicode.IsLetter(next):
		case r == ',' && unicode.IsDigit(prev) && unicode.IsDigit(next):
		case r == '.' && unicode.IsDigit(prev) && unicode.IsDigit(next):
			current.WriteRune(r)
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

func boilerplate(docs ...[]line) map[string]bool {
	repeated := map[string]bool{}
	for _, lines := range docs {
		counts := map[string]int{}
		for _, l := range lines {
			key := lineKey(l)
			counts[key]++
			if counts[key] >= boilerplateRepeats {
				repeated[key] = true
			}
		}
	}
	return repeated
}

func build(lines []line, skip map[string]bool) document {
	var doc document
	for _, l := range lines {
		if skip[lineKey(l)] {
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

func canonical(old, updated string) (document, document) {
	a, b := prepare(old), prepare(updated)
	skip := boilerplate(a, b)
	return build(a, skip), build(b, skip)
}

func Equivalent(old, updated string) bool {
	a, b := canonical(old, updated)
	if len(a.tokens) != len(b.tokens) {
		return false
	}
	for i := range a.tokens {
		if a.tokens[i].key != b.tokens[i].key {
			return false
		}
	}
	return true
}
