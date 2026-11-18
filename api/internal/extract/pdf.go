package extract

import (
	"bytes"
	"cmp"
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
)

func pdfText(body []byte) (text string, err error) {
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
	var b strings.Builder
	for i := 1; i <= reader.NumPage(); i++ {
		for _, line := range pageLines(reader.Page(i).Content().Text) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

type glyph struct {
	pdf.Text
	width float64
	known bool
}

func pageLines(texts []pdf.Text) []string {
	glyphs := make([]glyph, 0, len(texts))
	for _, t := range texts {
		if t.S == "" || t.S == "\n" || t.S == "\r" {
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
		glyphs = append(glyphs, g)
	}
	slices.SortStableFunc(glyphs, func(a, b glyph) int {
		if c := cmp.Compare(b.Y, a.Y); c != 0 {
			return c
		}
		return cmp.Compare(a.X, b.X)
	})

	var lines []string
	for start := 0; start < len(glyphs); {
		end := start + 1
		for end < len(glyphs) && math.Abs(glyphs[end].Y-glyphs[start].Y) <= lineTolerance*glyphs[start].FontSize {
			end++
		}
		line := glyphs[start:end]
		slices.SortStableFunc(line, func(a, b glyph) int { return cmp.Compare(a.X, b.X) })
		lines = append(lines, joinLine(line))
		start = end
	}
	return lines
}

func joinLine(line []glyph) string {
	var b strings.Builder
	for i, g := range line {
		if i > 0 && !strings.HasSuffix(b.String(), " ") {
			prev := line[i-1]
			threshold := gapWithKnownWidth
			if !prev.known {
				threshold = gapWithEstimatedWidth
			}
			if g.X-(prev.X+prev.width) > threshold*g.FontSize {
				b.WriteByte(' ')
			}
		}
		b.WriteString(g.S)
	}
	return b.String()
}
