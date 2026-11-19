package extract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

type matrix [3][3]float64

var identity = matrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (x matrix) mul(y matrix) matrix {
	var z matrix
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				z[i][j] += x[i][k] * y[k][j]
			}
		}
	}
	return z
}

func translation(tx, ty float64) matrix {
	return matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
}

type graphicsState struct {
	charSpacing float64
	scale       float64
	leading     float64
	font        *pdf.Font
	encoding    pdf.TextEncoding
	fontSize    float64
	rise        float64
	textMatrix  matrix
	lineMatrix  matrix
	ctm         matrix
}

const (
	maxContentBytes         = 32 << 20
	maxDocumentContentBytes = 4 * maxContentBytes
)

var errTooManyGlyphs = errors.New("too many glyphs")

type cachedFont struct {
	font     *pdf.Font
	encoding pdf.TextEncoding
}

type documentReader struct {
	maxGlyphs int
	fonts     map[string]cachedFont
	decoded   int64
}

func newDocumentReader(maxGlyphs int) *documentReader {
	return &documentReader{maxGlyphs: maxGlyphs, fonts: map[string]cachedFont{}}
}

func (d *documentReader) font(v pdf.Value) cachedFont {
	key := v.String()
	if f, ok := d.fonts[key]; ok {
		return f
	}
	font := &pdf.Font{V: v}
	f := cachedFont{font: font, encoding: fontEncoding(font)}
	d.fonts[key] = f
	return f
}

type pageContent struct {
	text      []pdf.Text
	truncated error
}

type pageReader struct {
	doc   *documentReader
	page  pdf.Page
	fonts map[string]cachedFont
	state graphicsState
	saved []graphicsState
	text  []pdf.Text
}

func (d *documentReader) pageText(page pdf.Page) (content pageContent, err error) {
	r := &pageReader{
		doc:   d,
		page:  page,
		fonts: map[string]cachedFont{},
		state: graphicsState{scale: 1, ctm: identity, font: &pdf.Font{}, encoding: identityEncoding{}},
	}
	defer func() {
		if rec := recover(); rec != nil {
			content.text = nil
			if e, ok := rec.(error); ok && errors.Is(e, errTooManyGlyphs) {
				err = fmt.Errorf("%w: more than %d glyphs", ErrLimitExceeded, d.maxGlyphs)
				return
			}
			err = fmt.Errorf("%v", rec)
		}
	}()
	contents, truncated, err := d.contentStream(page.V.Key("Contents"))
	if err != nil {
		return pageContent{}, err
	}
	content.truncated = truncated
	pdf.Interpret(contents, r.do)
	content.text = r.text
	return content, nil
}

func (d *documentReader) contentStream(contents pdf.Value) (pdf.Value, error, error) {
	parts := []pdf.Value{contents}
	if contents.Kind() == pdf.Array {
		parts = make([]pdf.Value, 0, contents.Len())
		for i := range contents.Len() {
			parts = append(parts, contents.Index(i))
		}
	}
	var (
		doc       bytes.Buffer
		decoded   int64
		truncated error
	)
	doc.WriteString(canonicalHeader)
	object := doc.Len()
	doc.WriteString("1 0 obj\n<< /Length ")
	lengthAt := doc.Len()
	fmt.Fprintf(&doc, "%010d >>\nstream\n", 0)
	start := doc.Len()
	for i, part := range parts {
		if part.Kind() != pdf.Stream {
			continue
		}
		if doc.Len() > start {
			doc.WriteByte('\n')
		}
		allowance := min(maxContentBytes-decoded, maxDocumentContentBytes-d.decoded)
		rc := part.Reader()
		n, err := io.Copy(&doc, io.LimitReader(rc, allowance+1))
		_ = rc.Close()
		decoded += n
		d.decoded += n
		switch {
		case decoded > maxContentBytes:
			return pdf.Value{}, nil, fmt.Errorf("%w: page content exceeds %d bytes", ErrLimitExceeded, maxContentBytes)
		case d.decoded > maxDocumentContentBytes:
			return pdf.Value{}, nil, fmt.Errorf("%w: document content exceeds %d bytes", ErrLimitExceeded, maxDocumentContentBytes)
		case err != nil && truncated == nil:
			truncated = fmt.Errorf("content stream %d: %w", i, err)
		}
	}
	copy(doc.Bytes()[lengthAt:], fmt.Sprintf("%010d", doc.Len()-start))
	doc.WriteString("\nendstream\nendobj\n")
	xref := doc.Len()
	fmt.Fprintf(&doc, "xref\n0 2\n0000000000 65535 f \n%010d 00000 n \ntrailer\n<< /Size 2 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", object, xref)
	r, err := pdf.NewReader(bytes.NewReader(doc.Bytes()), int64(doc.Len()))
	if err != nil {
		return pdf.Value{}, nil, err
	}
	return r.Trailer().Key("Root"), truncated, nil
}

type identityEncoding struct{}

func (identityEncoding) Decode(raw string) string { return raw }

func operands(stk *pdf.Stack) []pdf.Value {
	args := make([]pdf.Value, stk.Len())
	for i := len(args) - 1; i >= 0; i-- {
		args[i] = stk.Pop()
	}
	return args
}

func (r *pageReader) do(stk *pdf.Stack, op string) {
	args := operands(stk)
	g := &r.state
	switch op {
	case "q":
		r.saved = append(r.saved, r.state)
	case "Q":
		if n := len(r.saved); n > 0 {
			r.state, r.saved = r.saved[n-1], r.saved[:n-1]
		}
	case "cm":
		if m, ok := matrixOf(args); ok {
			g.ctm = m.mul(g.ctm)
		}
	case "BT":
		g.textMatrix, g.lineMatrix = identity, identity
	case "Tc":
		if len(args) == 1 {
			g.charSpacing = args[0].Float64()
		}
	case "Tz":
		if len(args) == 1 {
			g.scale = args[0].Float64() / 100
		}
	case "TL":
		if len(args) == 1 {
			g.leading = args[0].Float64()
		}
	case "Ts":
		if len(args) == 1 {
			g.rise = args[0].Float64()
		}
	case "Tf":
		if len(args) == 2 {
			r.setFont(args[0].Name(), args[1].Float64())
		}
	case "Tm":
		if m, ok := matrixOf(args); ok {
			g.textMatrix, g.lineMatrix = m, m
		}
	case "TD":
		if len(args) == 2 {
			g.leading = -args[1].Float64()
			r.moveLine(args[0].Float64(), args[1].Float64())
		}
	case "Td":
		if len(args) == 2 {
			r.moveLine(args[0].Float64(), args[1].Float64())
		}
	case "T*":
		r.moveLine(0, -g.leading)
	case "Tj":
		if len(args) == 1 {
			r.show(args[0].RawString())
		}
	case "'":
		if len(args) == 1 {
			r.moveLine(0, -g.leading)
			r.show(args[0].RawString())
		}
	case "\"":
		if len(args) == 3 {
			g.charSpacing = args[1].Float64()
			r.moveLine(0, -g.leading)
			r.show(args[2].RawString())
		}
	case "TJ":
		if len(args) == 1 {
			r.showArray(args[0])
		}
	}
}

func matrixOf(args []pdf.Value) (matrix, bool) {
	if len(args) != 6 {
		return matrix{}, false
	}
	var m matrix
	for i := range 6 {
		m[i/2][i%2] = args[i].Float64()
	}
	m[2][2] = 1
	return m, true
}

func (r *pageReader) setFont(name string, size float64) {
	f, ok := r.fonts[name]
	if !ok {
		f = r.doc.font(resources(r.page).Key("Font").Key(name))
		r.fonts[name] = f
	}
	r.state.font, r.state.encoding, r.state.fontSize = f.font, f.encoding, size
}

func resources(page pdf.Page) pdf.Value {
	v := page.V
	for range maxPageTreeDepth + 1 {
		if v.IsNull() {
			break
		}
		if res := v.Key("Resources"); !res.IsNull() {
			return res
		}
		v = v.Key("Parent")
	}
	return pdf.Value{}
}

func (r *pageReader) moveLine(tx, ty float64) {
	g := &r.state
	g.lineMatrix = translation(tx, ty).mul(g.lineMatrix)
	g.textMatrix = g.lineMatrix
}

func (r *pageReader) showArray(v pdf.Value) {
	g := &r.state
	for i := range v.Len() {
		item := v.Index(i)
		if item.Kind() == pdf.String {
			r.show(item.RawString())
			continue
		}
		tx := -item.Float64() / 1000 * g.fontSize * g.scale
		g.textMatrix = translation(tx, 0).mul(g.textMatrix)
	}
	r.text = append(r.text, pdf.Text{S: "\n"})
}

func (r *pageReader) show(raw string) {
	g := &r.state
	fontName := g.font.BaseFont()
	if i := strings.Index(fontName, "+"); i >= 0 {
		fontName = fontName[i+1:]
	}
	n := 0
	for _, ch := range g.encoding.Decode(raw) {
		var w0 float64
		if n < len(raw) {
			w0 = g.font.Width(int(raw[n]))
		}
		n++
		if len(r.text) >= r.doc.maxGlyphs {
			panic(errTooManyGlyphs)
		}
		trm := matrix{{g.fontSize * g.scale, 0, 0}, {0, g.fontSize, 0}, {0, g.rise, 1}}.mul(g.textMatrix).mul(g.ctm)
		r.text = append(r.text, pdf.Text{
			Font:     fontName,
			FontSize: trm[0][0],
			X:        trm[2][0],
			Y:        trm[2][1],
			W:        w0 / 1000 * trm[0][0],
			S:        string(ch),
		})
		tx := (w0/1000*g.fontSize + g.charSpacing) * g.scale
		g.textMatrix = translation(tx, 0).mul(g.textMatrix)
	}
}
