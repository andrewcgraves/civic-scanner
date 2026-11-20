package rctc

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type link struct {
	href string
	text string
}

type row struct {
	when  string
	name  string
	links []link
}

func (r row) empty() bool {
	return r.when == "" && r.name == "" && len(r.links) == 0
}

type cell struct {
	header bool
	text   string
	links  []link
}

type table struct {
	headers []string
	rows    [][]cell
	row     []cell
	inRow   bool
	cell    *cellBuilder
}

type cellBuilder struct {
	header bool
	text   strings.Builder
	links  []link
	anchor *link
	atext  strings.Builder
}

var (
	spaces     = regexp.MustCompile(`\s+`)
	nonLetters = regexp.MustCompile(`[^a-z]+`)
)

func normalize(s string) string {
	return strings.TrimSpace(spaces.ReplaceAllString(strings.ReplaceAll(s, " ", " "), " "))
}

func headerKey(s string) string {
	return strings.TrimSpace(nonLetters.ReplaceAllString(strings.ToLower(s), " "))
}

func (t *table) closeCell() {
	if t.cell == nil {
		return
	}
	c := t.cell
	if c.anchor != nil {
		c.closeAnchor()
	}
	t.cell = nil
	out := cell{header: c.header, text: normalize(c.text.String()), links: c.links}
	if out.header {
		t.headers = append(t.headers, out.text)
		return
	}
	t.row = append(t.row, out)
}

func (t *table) closeRow() {
	t.closeCell()
	if t.inRow && len(t.row) > 0 {
		t.rows = append(t.rows, t.row)
	}
	t.row = nil
	t.inRow = false
}

func (c *cellBuilder) closeAnchor() {
	c.anchor.text = normalize(c.atext.String())
	if c.anchor.href != "" {
		c.links = append(c.links, *c.anchor)
	}
	c.anchor = nil
	c.atext.Reset()
}

func (t *table) columns() (int, int, bool) {
	date, meeting := -1, -1
	for i, h := range t.headers {
		key := headerKey(h)
		switch {
		case key == "meeting" && meeting < 0:
			meeting = i
		case strings.HasPrefix(key, "date") && date < 0:
			date = i
		}
	}
	return date, meeting, date >= 0 && meeting >= 0
}

func (t *table) meetingRows() []row {
	date, meeting, _ := t.columns()
	out := make([]row, 0, len(t.rows))
	for _, cells := range t.rows {
		var r row
		if date < len(cells) {
			r.when = cells[date].text
		}
		if meeting < len(cells) {
			r.name = cells[meeting].text
			r.links = cells[meeting].links
		}
		out = append(out, r)
	}
	return out
}

func parseTables(body []byte) (int, []row) {
	z := html.NewTokenizer(bytes.NewReader(body))
	var (
		stack  []*table
		tables int
		rows   []row
	)
	finish := func(t *table) {
		t.closeRow()
		if _, _, ok := t.columns(); ok {
			tables++
			rows = append(rows, t.meetingRows()...)
		}
	}
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			for i := len(stack) - 1; i >= 0; i-- {
				finish(stack[i])
			}
			return tables, rows
		}
		var top *table
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			switch tok.DataAtom {
			case atom.Table:
				if tt == html.StartTagToken {
					stack = append(stack, &table{})
				}
			case atom.Tr:
				if top != nil {
					top.closeRow()
					top.inRow = true
				}
			case atom.Td, atom.Th:
				if top != nil {
					top.closeCell()
					if !top.inRow {
						top.inRow = true
					}
					top.cell = &cellBuilder{header: tok.DataAtom == atom.Th}
				}
			case atom.A:
				if top != nil && top.cell != nil {
					if top.cell.anchor != nil {
						top.cell.closeAnchor()
					}
					top.cell.anchor = &link{href: strings.TrimSpace(attr(tok, "href"))}
				}
			case atom.Br, atom.P, atom.Div:
				if top != nil && top.cell != nil {
					top.cell.text.WriteString(" ")
					if top.cell.anchor != nil {
						top.cell.atext.WriteString(" ")
					}
				}
			}
		case html.EndTagToken:
			tok := z.Token()
			switch tok.DataAtom {
			case atom.Table:
				if top != nil {
					stack = stack[:len(stack)-1]
					finish(top)
				}
			case atom.Tr:
				if top != nil {
					top.closeRow()
				}
			case atom.Td, atom.Th:
				if top != nil {
					top.closeCell()
				}
			case atom.A:
				if top != nil && top.cell != nil && top.cell.anchor != nil {
					top.cell.closeAnchor()
				}
			}
		case html.TextToken:
			if top != nil && top.cell != nil {
				text := string(z.Text())
				top.cell.text.WriteString(text)
				if top.cell.anchor != nil {
					top.cell.atext.WriteString(text)
				}
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
