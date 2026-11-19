package extract

import (
	"bytes"
	"fmt"
)

const (
	canonicalHeader = "%PDF-1.7\n"
	trailerWindow   = 1024
	startxref       = "startxref"
	endOfFile       = "%%EOF"
)

type patchedDocument struct {
	body    []byte
	patches map[int64]byte
}

func (d patchedDocument) Size() int64 {
	return int64(len(d.body))
}

func (d patchedDocument) ReadAt(p []byte, off int64) (int, error) {
	n, err := bytes.NewReader(d.body).ReadAt(p, off)
	for pos, c := range d.patches {
		if pos >= off && pos < off+int64(n) {
			p[pos-off] = c
		}
	}
	return n, err
}

func repair(body []byte) (patchedDocument, error) {
	if i := bytes.Index(body[:min(len(body), pdfHeaderWindow)], []byte("%PDF-")); i > 0 {
		body = body[i:]
	}
	if !validHeader(body) {
		return patchedDocument{}, fmt.Errorf("%w: pdf: not a PDF file: invalid header", ErrMalformed)
	}
	body = trimAfterEOF(body)
	doc := patchedDocument{body: body, patches: map[int64]byte{}}
	for i := range len(canonicalHeader) {
		doc.patches[int64(i)] = canonicalHeader[i]
	}
	tail := max(0, len(body)-trailerWindow)
	if i := bytes.LastIndex(body[tail:], []byte(startxref)); i > 0 {
		at := tail + i
		if isBlank(body[at-1]) {
			doc.patches[int64(at-1)] = '\n'
		}
		if after := at + len(startxref); after < len(body) && isBlank(body[after]) {
			doc.patches[int64(after)] = '\n'
		}
	}
	return doc, nil
}

func validHeader(body []byte) bool {
	if len(body) < len(canonicalHeader) || !bytes.HasPrefix(body, []byte("%PDF-")) {
		return false
	}
	major, dot, minor, end := body[5], body[6], body[7], body[8]
	return (major == '1' || major == '2') && dot == '.' && minor >= '0' && minor <= '9' &&
		(isPDFWhitespace(end) || end == '%')
}

func trimAfterEOF(body []byte) []byte {
	tail := max(0, len(body)-trailerWindow)
	i := bytes.LastIndex(body[tail:], []byte(endOfFile))
	if i < 0 {
		return body
	}
	end := tail + i + len(endOfFile)
	if len(bytes.TrimLeft(body[end:], "\t\n\r ")) == 0 {
		return body
	}
	return body[:end]
}

func isBlank(c byte) bool {
	return c == ' ' || c == '\t'
}

func isPDFWhitespace(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}
