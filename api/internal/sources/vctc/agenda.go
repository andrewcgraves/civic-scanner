package vctc

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type link struct {
	href string
	text string
}

type section struct {
	heading string
	id      string
	links   []link
}

var (
	agendaHeading       = regexp.MustCompile(`^((current|amended|revised|updated) )*agenda$`)
	presentationHeading = regexp.MustCompile(`^presentations?$`)
	nonLetters          = regexp.MustCompile(`[^a-z]+`)
)

func headingKey(text string) string {
	return strings.TrimSpace(nonLetters.ReplaceAllString(strings.ToLower(text), " "))
}

func (s section) agenda() bool {
	return s.id == "current-agenda" || agendaHeading.MatchString(s.heading)
}

func (s section) presentations() bool {
	return presentationHeading.MatchString(s.heading)
}

func sections(content string) []section {
	z := html.NewTokenizer(strings.NewReader(content))
	var (
		out       []section
		heading   *strings.Builder
		headingID string
		anchor    *link
		anchorTxt strings.Builder
	)
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken:
			tok := z.Token()
			switch {
			case isHeading(tok.DataAtom):
				heading = &strings.Builder{}
				headingID = attr(tok, "id")
			case tok.DataAtom == atom.A && heading == nil && len(out) > 0:
				anchor = &link{href: strings.TrimSpace(attr(tok, "href"))}
				anchorTxt.Reset()
			}
		case html.EndTagToken:
			tok := z.Token()
			switch {
			case isHeading(tok.DataAtom) && heading != nil:
				out = append(out, section{heading: headingKey(heading.String()), id: strings.ToLower(strings.TrimSpace(headingID))})
				heading = nil
			case tok.DataAtom == atom.A && anchor != nil:
				anchor.text = normalize(anchorTxt.String())
				if anchor.href != "" {
					last := &out[len(out)-1]
					last.links = append(last.links, *anchor)
				}
				anchor = nil
			}
		case html.TextToken:
			text := string(z.Text())
			if heading != nil {
				heading.WriteString(text)
			}
			if anchor != nil {
				anchorTxt.WriteString(text)
			}
		}
	}
}

func attr(tok html.Token, key string) string {
	for _, a := range tok.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func isHeading(a atom.Atom) bool {
	switch a {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return false
}
