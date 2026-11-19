package extract

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract/extracttest"
)

var agendaBody = strings.Repeat("Item 1. Call to order and roll call of the Board of Directors. ", 5)

func extractor() *Extractor {
	return &Extractor{PDF: InProcess{}, MinChars: 1}
}

func textOf(r Result, err error) (string, error) {
	return r.Text, err
}

func TestNormalizeCollapsesFormattingNoise(t *testing.T) {
	cases := map[string]string{
		"":                                    "",
		"  \n\t\n":                            "",
		"Board  Meeting\r\n\r\n  Item\t1  ":   "Board Meeting\nItem 1",
		"a\u00a0b\u2003c":                     "a b c",
		"line\u2028next\fpage\vtab":           "line\nnext\npage\ntab",
		"nul\x00byte\x07bell\ufeffbom\u00adx": "nulbytebellbomx",
		"bad \xff utf8":                       "bad utf8",
		"zero\u200bwidth":                     "zerowidth",
		"LOCATION \u2013\ufffd 1619 EDITH":    "LOCATION \u2013 1619 EDITH",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeMakesWhitespaceOnlyEditsEqual(t *testing.T) {
	a := Normalize("Item 1.  Call to Order\n\nItem 2. Roll Call")
	b := Normalize("  Item 1. Call to Order \r\nItem 2.\tRoll Call\n\n\n")
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}

func TestTextHandlesPlainAndMarkdown(t *testing.T) {
	for _, ct := range []string{"text/plain; charset=utf-8", "text/markdown", ""} {
		got, err := textOf(extractor().Text(t.Context(), Request{ContentType: ct, Body: []byte("# Agenda\n\n1. Call to order\n")}))
		if err != nil {
			t.Fatalf("Text(%q): %v", ct, err)
		}
		if got != "# Agenda\n1. Call to order" {
			t.Errorf("Text(%q) = %q", ct, got)
		}
	}
}

func TestTextDecodesPlainTextByCharset(t *testing.T) {
	got, err := textOf(extractor().Text(t.Context(), Request{ContentType: "text/plain; charset=iso-8859-1", Body: []byte("Caf\xe9 Agenda")}))
	if err != nil || got != "Café Agenda" {
		t.Errorf("Text = %q, %v", got, err)
	}
}

func TestTextRejectsUnsupportedTypes(t *testing.T) {
	cases := map[string][]byte{
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": []byte("PK\x03\x04docx"),
		"image/png":                []byte("\x89PNG\r\n\x1a\n"),
		"application/octet-stream": []byte("PK\x03\x04zip"),
		"not a media type":         []byte("\x00\x01\x02"),
	}
	for ct, body := range cases {
		if _, err := extractor().Text(t.Context(), Request{ContentType: ct, Body: body}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Text(%q) err = %v, want ErrUnsupported", ct, err)
		}
	}
}

func TestTextBelowThePlausibilityFloorIsNoText(t *testing.T) {
	e := &Extractor{PDF: InProcess{}}
	cases := map[string]Request{
		"whitespace":   {ContentType: "text/plain", Body: []byte(" \n\t ")},
		"script only":  {ContentType: "text/html", Body: []byte("<html><body><script>var a</script></body></html>")},
		"short page":   {ContentType: "text/html", Body: []byte("<html><body><p>Agenda to follow.</p></body></html>")},
		"short pdf":    {ContentType: "application/pdf", Body: extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET")},
		"under 200":    {ContentType: "text/plain", Body: []byte(strings.Repeat("x", DefaultMinChars-1))},
		"spaces count": {ContentType: "text/plain", Body: []byte(strings.Repeat("x ", DefaultMinChars-1))},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := e.Text(t.Context(), req); !errors.Is(err, ErrNoText) {
				t.Errorf("err = %v, want ErrNoText", err)
			}
		})
	}
	if _, err := e.Text(t.Context(), Request{ContentType: "text/plain", Body: []byte(strings.Repeat("x", DefaultMinChars))}); err != nil {
		t.Errorf("exactly the floor: %v", err)
	}
}

func TestTextWithoutAPDFReaderIsUnavailable(t *testing.T) {
	_, err := (&Extractor{}).Text(t.Context(), Request{ContentType: "application/pdf", Body: []byte("%PDF-1.4\n")})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestTextPassesPDFErrorsThrough(t *testing.T) {
	_, err := extractor().Text(t.Context(), Request{ContentType: "application/pdf", Body: []byte("%PDF-1.4\ntruncated")})
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed", err)
	}
}

func TestTextTreatsInterstitialsAsBlocked(t *testing.T) {
	page, err := os.ReadFile("testdata/imperva_interstitial.html")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]Request{
		"html interstitial":          {ContentType: "text/html; charset=utf-8", Body: page},
		"html for a pdf url":         {ContentType: "text/html", Body: []byte("<html><body><p>" + agendaBody + "</p></body></html>"), ExpectPDF: true},
		"markdown interstitial":      {ContentType: "text/markdown", Body: []byte("# Pardon Our Interruption\n\nAs you were browsing something about your browser made us think you were a bot.")},
		"cloudflare challenge":       {ContentType: "text/html", Body: []byte(`<html><head><title>Just a moment...</title></head><body><script src="/cdn-cgi/challenge-platform/h/b/orchestrate/jsch/v1"></script>` + agendaBody + `</body></html>`)},
		"akamai denial":              {ContentType: "text/html", Body: []byte("<html><body><h1>Access Denied</h1>You don't have permission to access \"http://agency.test/agenda.pdf\" on this server.</body></html>")},
		"octet-stream sniffed html":  {ContentType: "application/octet-stream", Body: page},
		"html interstitial expected": {ContentType: "text/html", Body: page, ExpectPDF: true},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := extractor().Text(t.Context(), req); !errors.Is(err, ErrBlocked) {
				t.Errorf("err = %v, want ErrBlocked", err)
			}
		})
	}
}

func TestTextAcceptsMarkdownForAPDFURL(t *testing.T) {
	got, err := textOf(extractor().Text(t.Context(), Request{ContentType: "text/markdown", Body: []byte(agendaBody), ExpectPDF: true}))
	if err != nil || !strings.HasPrefix(got, "Item 1.") {
		t.Errorf("Text = %q, %v", got, err)
	}
}

func TestDetectSniffsWhenTheTypeIsMissingOrGeneric(t *testing.T) {
	cases := []struct {
		contentType string
		body        string
		want        kind
	}{
		{"", "<!DOCTYPE html><html><body>x</body></html>", kindHTML},
		{"application/octet-stream", "<html><body>x</body></html>", kindHTML},
		{"application/xhtml+xml", "<html/>", kindHTML},
		{"application/pdf", "not a pdf", kindPDF},
		{"text/html", "%PDF-1.4\n", kindPDF},
		{"", "plain words", kindPlain},
	}
	for _, tc := range cases {
		if got := detect(tc.contentType, []byte(tc.body)); got != tc.want {
			t.Errorf("detect(%q, %q) = %v, want %v", tc.contentType, tc.body, got, tc.want)
		}
	}
}
