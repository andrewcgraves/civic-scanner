package vctc

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type link struct {
	href string
	text string
}

const agendaHeading = "current agenda"

func agendaLinks(content string) []link {
	z := html.NewTokenizer(strings.NewReader(content))
	var (
		links     []link
		heading   *strings.Builder
		anchor    *link
		anchorTxt strings.Builder
		inSection bool
	)
	for {
		switch z.Next() {
		case html.ErrorToken:
			return links
		case html.StartTagToken:
			tok := z.Token()
			switch {
			case isHeading(tok.DataAtom):
				heading = &strings.Builder{}
			case tok.DataAtom == atom.A && inSection && heading == nil:
				anchor = &link{}
				anchorTxt.Reset()
				for _, attr := range tok.Attr {
					if attr.Key == "href" {
						anchor.href = strings.TrimSpace(attr.Val)
					}
				}
			}
		case html.EndTagToken:
			tok := z.Token()
			switch {
			case isHeading(tok.DataAtom) && heading != nil:
				text := strings.ToLower(normalize(heading.String()))
				heading = nil
				if inSection {
					return links
				}
				inSection = text == agendaHeading
			case tok.DataAtom == atom.A && anchor != nil:
				anchor.text = normalize(anchorTxt.String())
				if anchor.href != "" {
					links = append(links, *anchor)
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

func isHeading(a atom.Atom) bool {
	switch a {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return false
}
