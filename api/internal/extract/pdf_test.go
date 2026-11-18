package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract/extracttest"
)

func flateBombPDF(t testing.TB, decompressed int) []byte {
	t.Helper()
	doc, err := extracttest.FlateBomb(decompressed)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func inProcessText(t *testing.T, doc []byte) (string, error) {
	t.Helper()
	text, err := PDFText(t.Context(), doc, Limits{})
	return Normalize(text), err
}

func TestBuildPDFIsReadable(t *testing.T) {
	doc := extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Hello) Tj ET")
	r, err := pdf.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if r.NumPage() != 1 {
		t.Fatalf("NumPage = %d", r.NumPage())
	}
}

func TestPDFTextReadsLinesTopToBottomAcrossPages(t *testing.T) {
	doc := extracttest.PDF(false,
		"BT /F1 12 Tf 72 700 Td (Item 2. Approve Minutes) Tj ET BT /F1 12 Tf 72 720 Td (Regular Board Meeting) Tj ET",
		"BT /F1 12 Tf 72 720 Td (Item 3. Public Comment) Tj ET",
	)
	got, err := inProcessText(t, doc)
	if err != nil {
		t.Fatalf("PDFText: %v", err)
	}
	if want := "Regular Board Meeting\nItem 2. Approve Minutes\nItem 3. Public Comment"; got != want {
		t.Errorf("PDFText = %q, want %q", got, want)
	}
}

func TestPDFTextSpacesWordsSeparatedByPositioning(t *testing.T) {
	doc := extracttest.PDF(true, "BT /F1 10 Tf 72 720 Td [(Consent)-400(Calendar)] TJ ET BT /F1 10 Tf 72 700 Td [(Ag)-20(enda)] TJ ET")
	got, err := inProcessText(t, doc)
	if err != nil {
		t.Fatalf("PDFText: %v", err)
	}
	if want := "Consent Calendar\nAgenda"; got != want {
		t.Errorf("PDFText = %q, want %q", got, want)
	}
}

func TestPDFTextKeepsStreamOrderWithinABaseline(t *testing.T) {
	doc := extracttest.PDF(true, "BT /F1 10 Tf 72 720 Td (To request) Tj 20 0 Td (to speak) Tj ET BT /F1 10 Tf 300 720 Td (DISTRICT 5) Tj ET")
	got, err := inProcessText(t, doc)
	if err != nil {
		t.Fatalf("PDFText: %v", err)
	}
	if want := "To request to speak DISTRICT 5"; got != want {
		t.Errorf("PDFText = %q, want %q", got, want)
	}
}

func TestPDFTextSkipsLeadingJunkBeforeTheHeader(t *testing.T) {
	doc := append([]byte("\xef\xbb\xbfjunk\n"), extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET")...)
	got, err := inProcessText(t, doc)
	if err != nil || got != "Agenda" {
		t.Errorf("PDFText = %q, %v", got, err)
	}
}

func TestPDFWithMostlyEmptyPagesIsNoText(t *testing.T) {
	cases := map[string][]byte{
		"no text":       extracttest.PDF(false, "0 0 m 100 100 l S"),
		"form xobjects": extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET", "/Fm1 Do", "/Fm2 Do"),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := inProcessText(t, doc); !errors.Is(err, ErrNoText) {
				t.Errorf("err = %v, want ErrNoText", err)
			}
		})
	}
}

func TestMalformedPDFIsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"truncated":       extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET")[:200],
		"bad content":     extracttest.PDF(false, "BT /F1 Tf (Agenda) Tj ET"),
		"pdf 2.0":         []byte("%PDF-2.0\nnot really\n%%EOF\n"),
		"self reference":  extracttest.SelfReferencingPages(),
		"no pages object": extracttest.Assemble("<< /Type /Catalog >>"),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := inProcessText(t, doc); !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrNoText) {
				t.Errorf("err = %v, want ErrMalformed or ErrNoText", err)
			}
		})
	}
}

func TestPDFTextBoundsPagesGlyphsAndText(t *testing.T) {
	if _, err := PDFText(t.Context(), extracttest.HugeCount(), Limits{}); !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "2000000000") {
		t.Errorf("huge /Count: err = %v", err)
	}
	many := extracttest.PDF(false, "BT /F1 12 Tf 72 720 Td (One) Tj ET", "BT /F1 12 Tf 72 720 Td (Two) Tj ET", "BT /F1 12 Tf 72 720 Td (Three) Tj ET")
	understated := bytes.Replace(many, []byte("/Count 3"), []byte("/Count 1"), 1)
	if _, err := PDFText(t.Context(), understated, Limits{MaxPages: 2}); !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("page limit: err = %v", err)
	}
	if _, err := PDFText(t.Context(), many, Limits{MaxGlyphsPerPage: 2}); !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("glyph limit: err = %v", err)
	}
	if _, err := PDFText(t.Context(), many, Limits{MaxTextBytes: 5}); !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("text limit: err = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := PDFText(ctx, many, Limits{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v", err)
	}
}

func TestPageTreeIsBoundedByDepthAndNodes(t *testing.T) {
	deep := []string{"<< /Type /Catalog /Pages 2 0 R >>"}
	for i := 2; i < 2+maxPageTreeDepth+2; i++ {
		deep = append(deep, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] >>", i+1))
	}
	deep = append(deep, "<< /Type /Page >>")
	if _, err := PDFText(t.Context(), extracttest.Assemble(deep...), Limits{}); !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "deeper") {
		t.Errorf("deep tree: err = %v", err)
	}
	if _, err := PDFText(t.Context(), extracttest.SelfReferencingPages(), Limits{}); !errors.Is(err, ErrMalformed) {
		t.Errorf("self reference: err = %v", err)
	}
}

func TestPageLinesEstimatesUnusableWidths(t *testing.T) {
	texts := []pdf.Text{
		{FontSize: 10, X: 0, Y: 100, W: 0, S: "a"},
		{FontSize: 10, X: 5, Y: 100, W: 0, S: "b"},
		{FontSize: 10, X: 20, Y: 100, W: 0, S: "c"},
		{FontSize: 10, X: 0, Y: 100, W: 0, S: "d"},
		{FontSize: 0, X: 0, Y: 50, W: 0, S: "z"},
		{FontSize: 10, X: 0, Y: 30, W: 0, S: "\n"},
	}
	got := pageLines(texts)
	if strings.Join(got, "|") != "ab c d|z" {
		t.Errorf("pageLines = %q", got)
	}
}

func TestLinesBuildsOneTextLinePerArgument(t *testing.T) {
	got, err := inProcessText(t, extracttest.Lines("1. Call to Order (in person)", `Item 2\3`))
	if err != nil || got != "1. Call to Order (in person)\nItem 2\\3" {
		t.Errorf("PDFText = %q, %v", got, err)
	}
}
