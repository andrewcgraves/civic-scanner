package extract

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/ledongthuc/pdf"
)

const (
	lineTolerance         = 0.5
	gapWithKnownWidth     = 0.15
	gapWithEstimatedWidth = 0.45
	estimatedGlyphWidth   = 0.5
	maxPageTreeDepth      = 32
	minBytesPerPage       = 40
)

type Limits struct {
	MaxPages         int
	MaxGlyphsPerPage int
	MaxTextBytes     int64
}

var DefaultLimits = Limits{MaxPages: 2000, MaxGlyphsPerPage: 200_000, MaxTextBytes: 8 << 20}

func (l Limits) withDefaults() Limits {
	if l.MaxPages <= 0 {
		l.MaxPages = DefaultLimits.MaxPages
	}
	if l.MaxGlyphsPerPage <= 0 {
		l.MaxGlyphsPerPage = DefaultLimits.MaxGlyphsPerPage
	}
	if l.MaxTextBytes <= 0 {
		l.MaxTextBytes = DefaultLimits.MaxTextBytes
	}
	return l
}

type InProcess struct {
	Limits Limits
}

func (p InProcess) ReadPDF(ctx context.Context, body []byte) (string, error) {
	return PDFText(ctx, body, p.Limits)
}

func PDFText(ctx context.Context, body []byte, limits Limits) (text string, err error) {
	limits = limits.withDefaults()
	if i := bytes.Index(body[:min(len(body), pdfHeaderWindow)], []byte("%PDF-")); i > 0 {
		body = body[i:]
	}
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("%w: pdf: %v", ErrMalformed, r)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("%w: pdf: %w", ErrMalformed, err)
	}
	maxPages := min(limits.MaxPages, max(1, len(body)/minBytesPerPage))
	pages, err := pageTree(reader, maxPages)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	empty := 0
	for i, page := range pages {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		glyphs := page.Content().Text
		if len(glyphs) > limits.MaxGlyphsPerPage {
			return "", fmt.Errorf("%w: page %d has %d glyphs, limit %d", ErrLimitExceeded, i+1, len(glyphs), limits.MaxGlyphsPerPage)
		}
		lines := pageLines(glyphs)
		if visibleChars(strings.Join(lines, "")) == 0 {
			empty++
		}
		for _, line := range lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if int64(b.Len()) > limits.MaxTextBytes {
			return "", fmt.Errorf("%w: text exceeds %d bytes", ErrLimitExceeded, limits.MaxTextBytes)
		}
	}
	if len(pages) == 0 || empty*2 > len(pages) {
		return "", fmt.Errorf("%w: %d of %d pages have no text layer (scanned pages and text inside form XObjects are not read)", ErrNoText, empty, len(pages))
	}
	return b.String(), nil
}

func pageTree(reader *pdf.Reader, maxPages int) ([]pdf.Page, error) {
	var (
		pages  []pdf.Page
		visits int
	)
	budget := 4*maxPages + 16
	var walk func(node pdf.Value, depth int) error
	walk = func(node pdf.Value, depth int) error {
		visits++
		if visits > budget {
			return fmt.Errorf("%w: page tree has more than %d nodes", ErrMalformed, budget)
		}
		if depth > maxPageTreeDepth {
			return fmt.Errorf("%w: page tree is deeper than %d levels", ErrMalformed, maxPageTreeDepth)
		}
		kids := node.Key("Kids")
		if kids.Kind() != pdf.Array {
			if len(pages) >= maxPages {
				return fmt.Errorf("%w: more than %d pages", ErrLimitExceeded, maxPages)
			}
			pages = append(pages, pdf.Page{V: node})
			return nil
		}
		for i := range kids.Len() {
			if err := walk(kids.Index(i), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	root := reader.Trailer().Key("Root").Key("Pages")
	if declared := root.Key("Count").Int64(); declared > int64(maxPages) {
		return nil, fmt.Errorf("%w: page tree declares %d pages, at most %d are plausible", ErrMalformed, declared, maxPages)
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}
	return pages, nil
}

type glyph struct {
	pdf.Text
	width float64
	known bool
}

type line struct {
	y      float64
	size   float64
	glyphs []glyph
}

func pageLines(texts []pdf.Text) []string {
	var lines []*line
	for _, t := range texts {
		if strings.TrimRight(t.S, "\r\n") == "" {
			continue
		}
		size := math.Abs(t.FontSize)
		if size == 0 {
			size = 1
		}
		g := glyph{Text: t, width: t.W, known: true}
		g.FontSize = size
		if t.W <= 0 || t.W > 2*size {
			g.width, g.known = estimatedGlyphWidth*size, false
		}
		target := -1
		for i := len(lines) - 1; i >= 0; i-- {
			if math.Abs(lines[i].y-g.Y) <= lineTolerance*lines[i].size {
				target = i
				break
			}
		}
		if target < 0 {
			lines = append(lines, &line{y: g.Y, size: size})
			target = len(lines) - 1
		}
		lines[target].glyphs = append(lines[target].glyphs, g)
	}
	slices.SortStableFunc(lines, func(a, b *line) int { return cmp.Compare(b.y, a.y) })
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, joinLine(l.glyphs))
	}
	return out
}

func joinLine(glyphs []glyph) string {
	var b strings.Builder
	for i, g := range glyphs {
		if i > 0 && !strings.HasSuffix(b.String(), " ") && g.S != " " {
			prev := glyphs[i-1]
			threshold := gapWithKnownWidth
			if !prev.known {
				threshold = gapWithEstimatedWidth
			}
			gap := g.X - (prev.X + prev.width)
			if gap > threshold*g.FontSize || gap < -2*g.FontSize {
				b.WriteByte(' ')
			}
		}
		b.WriteString(g.S)
	}
	return b.String()
}
