package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

var (
	ErrUnsupported   = errors.New("extract: unsupported document type")
	ErrMalformed     = errors.New("extract: malformed document")
	ErrNoText        = errors.New("extract: document has too little text")
	ErrBlocked       = errors.New("extract: got a bot check or error page instead of the document")
	ErrLimitExceeded = errors.New("extract: document exceeds extraction limits")
	ErrUnavailable   = errors.New("extract: pdf extractor unavailable")
)

const (
	pdfHeaderWindow = 1024
	DefaultMinChars = 200
)

type PDFReader interface {
	ReadPDF(ctx context.Context, body []byte) (Document, error)
}

type Result struct {
	Text string
	PDF  *Report
}

type Extractor struct {
	PDF      PDFReader
	MinChars int
}

type Request struct {
	ContentType string
	Body        []byte
	ExpectPDF   bool
}

func (e *Extractor) Text(ctx context.Context, req Request) (Result, error) {
	var (
		raw    string
		report *Report
	)
	switch detect(req.ContentType, req.Body) {
	case kindPDF:
		if e.PDF == nil {
			return Result{}, ErrUnavailable
		}
		doc, err := e.PDF.ReadPDF(ctx, req.Body)
		if err != nil {
			return Result{}, err
		}
		raw, report = doc.Text, &doc.Report
	case kindHTML:
		if req.ExpectPDF {
			return Result{}, fmt.Errorf("%w: expected a PDF, got an HTML page", ErrBlocked)
		}
		if marker, ok := Interstitial(req.Body); ok {
			return Result{}, fmt.Errorf("%w: page contains %q", ErrBlocked, marker)
		}
		var err error
		if raw, err = htmlText(req.ContentType, req.Body); err != nil {
			return Result{}, err
		}
	case kindPlain:
		if marker, ok := Interstitial(req.Body); ok {
			return Result{}, fmt.Errorf("%w: text contains %q", ErrBlocked, marker)
		}
		raw = decode(req.ContentType, req.Body)
	default:
		return Result{}, fmt.Errorf("%w: %q", ErrUnsupported, req.ContentType)
	}
	text := Normalize(raw)
	minChars := e.MinChars
	if minChars <= 0 {
		minChars = DefaultMinChars
	}
	if n := visibleChars(text); n < minChars {
		return Result{}, fmt.Errorf("%w: %d characters, want at least %d", ErrNoText, n, minChars)
	}
	return Result{Text: text, PDF: report}, nil
}

func visibleChars(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

type kind int

const (
	kindUnknown kind = iota
	kindPDF
	kindHTML
	kindPlain
)

func detect(contentType string, body []byte) kind {
	if bytes.Contains(body[:min(len(body), pdfHeaderWindow)], []byte("%PDF-")) {
		return kindPDF
	}
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil || media == "" || media == "application/octet-stream" {
		media, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	switch media {
	case "application/pdf":
		return kindPDF
	case "text/html", "application/xhtml+xml":
		return kindHTML
	case "text/plain", "text/markdown":
		return kindPlain
	}
	return kindUnknown
}

func decode(contentType string, body []byte) string {
	enc, _, _ := charset.DetermineEncoding(body, contentType)
	decoded, err := enc.NewDecoder().Bytes(body)
	if err != nil {
		return string(body)
	}
	return string(decoded)
}

func Normalize(s string) string {
	s = strings.ToValidUTF8(s, "")
	var (
		out  strings.Builder
		line strings.Builder
	)
	flush := func() {
		text := strings.TrimSpace(line.String())
		line.Reset()
		if text == "" {
			return
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(text)
	}
	pendingSpace := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\n' || r == '\r' || r == '\f' || r == '\v' || r == '\u2028' || r == '\u2029':
			pendingSpace = false
			flush()
		case unicode.IsSpace(r):
			pendingSpace = true
		case unicode.IsControl(r) || r == utf8.RuneError || r == '\ufeff' || r == '\u00ad' || r == '\u200b':
		default:
			if pendingSpace && line.Len() > 0 {
				line.WriteByte(' ')
			}
			pendingSpace = false
			line.WriteRune(r)
		}
	}
	flush()
	return out.String()
}
