package extract

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrUnsupported = errors.New("extract: unsupported document type")
	ErrMalformed   = errors.New("extract: malformed document")
	ErrNoText      = errors.New("extract: document has no text")
)

const pdfHeaderWindow = 1024

func Text(contentType string, body []byte) (string, error) {
	var (
		raw string
		err error
	)
	switch kind := detect(contentType, body); kind {
	case kindPDF:
		raw, err = pdfText(body)
	case kindHTML:
		raw, err = htmlText(contentType, body)
	case kindPlain:
		raw = string(body)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupported, contentType)
	}
	if err != nil {
		return "", err
	}
	text := Normalize(raw)
	if text == "" {
		return "", ErrNoText
	}
	return text, nil
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
		case unicode.IsControl(r) || r == '\ufeff' || r == '\u00ad' || r == '\u200b':
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
