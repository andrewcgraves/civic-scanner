package extract

import (
	"errors"
	"testing"
)

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
		got, err := Text(ct, []byte("# Agenda\n\n1. Call to order\n"))
		if err != nil {
			t.Fatalf("Text(%q): %v", ct, err)
		}
		if got != "# Agenda\n1. Call to order" {
			t.Errorf("Text(%q) = %q", ct, got)
		}
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
		if _, err := Text(ct, body); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Text(%q) err = %v, want ErrUnsupported", ct, err)
		}
	}
}

func TestTextWithOnlyWhitespaceIsNoText(t *testing.T) {
	if _, err := Text("text/plain", []byte(" \n\t ")); !errors.Is(err, ErrNoText) {
		t.Errorf("err = %v, want ErrNoText", err)
	}
	if _, err := Text("text/html", []byte("<html><head><title>x</title></head><body><script>var a</script></body></html>")); !errors.Is(err, ErrNoText) {
		t.Errorf("html err = %v, want ErrNoText", err)
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
