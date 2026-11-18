package extract

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var skippedElements = map[atom.Atom]bool{
	atom.Head:     true,
	atom.Script:   true,
	atom.Style:    true,
	atom.Noscript: true,
	atom.Template: true,
	atom.Svg:      true,
	atom.Iframe:   true,
	atom.Object:   true,
}

var blockElements = map[atom.Atom]bool{
	atom.Address: true, atom.Article: true, atom.Aside: true, atom.Blockquote: true,
	atom.Br: true, atom.Caption: true, atom.Dd: true, atom.Div: true, atom.Dl: true,
	atom.Dt: true, atom.Fieldset: true, atom.Figcaption: true, atom.Figure: true,
	atom.Footer: true, atom.Form: true, atom.H1: true, atom.H2: true, atom.H3: true,
	atom.H4: true, atom.H5: true, atom.H6: true, atom.Header: true, atom.Hr: true,
	atom.Li: true, atom.Main: true, atom.Nav: true, atom.Ol: true, atom.P: true,
	atom.Pre: true, atom.Section: true, atom.Table: true, atom.Tbody: true,
	atom.Tfoot: true, atom.Thead: true, atom.Tr: true,
	atom.Ul: true,
}

func htmlText(contentType string, body []byte) (string, error) {
	doc, err := html.Parse(strings.NewReader(decode(contentType, body)))
	if err != nil {
		return "", fmt.Errorf("%w: html: %w", ErrMalformed, err)
	}
	var b strings.Builder
	writeNode(&b, doc)
	return b.String(), nil
}

func writeNode(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(n.Data)
		return
	case html.CommentNode, html.DoctypeNode:
		return
	case html.ElementNode:
		if skippedElements[n.DataAtom] {
			return
		}
	}
	block := n.Type == html.ElementNode && blockElements[n.DataAtom]
	if block {
		b.WriteByte('\n')
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) && c.PrevSibling != nil {
			b.WriteByte(' ')
		}
		writeNode(b, c)
	}
	if block {
		b.WriteByte('\n')
	}
}
