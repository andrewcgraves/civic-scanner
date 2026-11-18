package extract

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

type pdfFont struct {
	widths bool
}

func buildPDF(font pdfFont, pages ...string) []byte {
	fontDict := "<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>"
	if font.widths {
		widths := strings.TrimSpace(strings.Repeat("600 ", 126-32+1))
		fontDict = "<< /Type /Font /Subtype /Type1 /BaseFont /Courier /FirstChar 32 /LastChar 126 /Widths [" + widths + "] >>"
	}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"",
		fontDict,
	}
	kids := make([]string, 0, len(pages))
	for _, content := range pages {
		pageNum := len(objects) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageNum))
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", pageNum+1),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		)
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

func TestBuildPDFIsReadable(t *testing.T) {
	doc := buildPDF(pdfFont{}, "BT /F1 12 Tf 72 720 Td (Hello) Tj ET")
	r, err := pdf.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if r.NumPage() != 1 {
		t.Fatalf("NumPage = %d", r.NumPage())
	}
}

func TestPDFTextReadsLinesTopToBottomAcrossPages(t *testing.T) {
	doc := buildPDF(pdfFont{},
		"BT /F1 12 Tf 72 700 Td (Item 2. Approve Minutes) Tj ET BT /F1 12 Tf 72 720 Td (Regular Board Meeting) Tj ET",
		"BT /F1 12 Tf 72 720 Td (Item 3. Public Comment) Tj ET",
	)
	got, err := Text("application/pdf", doc)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	want := "Regular Board Meeting\nItem 2. Approve Minutes\nItem 3. Public Comment"
	if got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestPDFTextSpacesWordsSeparatedByPositioning(t *testing.T) {
	doc := buildPDF(pdfFont{widths: true},
		"BT /F1 10 Tf 72 720 Td [(Consent)-400(Calendar)] TJ ET BT /F1 10 Tf 72 700 Td [(Ag)-20(enda)] TJ ET")
	got, err := Text("application/pdf", doc)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if want := "Consent Calendar\nAgenda"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestPDFTextJoinsGlyphsOnTheSameBaseline(t *testing.T) {
	doc := buildPDF(pdfFont{widths: true},
		"BT /F1 10 Tf 200 720 Td (Recommendation) Tj ET BT /F1 10 Tf 72 721 Td (1.) Tj ET")
	got, err := Text("application/pdf", doc)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if want := "1. Recommendation"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestPDFTextSkipsLeadingJunkBeforeTheHeader(t *testing.T) {
	doc := append([]byte("\xef\xbb\xbfjunk\n"), buildPDF(pdfFont{}, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET")...)
	got, err := Text("application/octet-stream", doc)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if got != "Agenda" {
		t.Errorf("Text = %q", got)
	}
}

func TestPDFWithoutTextIsNoText(t *testing.T) {
	doc := buildPDF(pdfFont{}, "0 0 m 100 100 l S")
	if _, err := Text("application/pdf", doc); !errors.Is(err, ErrNoText) {
		t.Errorf("err = %v, want ErrNoText", err)
	}
}

func TestMalformedPDFIsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"truncated":   buildPDF(pdfFont{}, "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET")[:200],
		"bad content": buildPDF(pdfFont{}, "BT /F1 Tf (Agenda) Tj ET"),
		"pdf 2.0":     []byte("%PDF-2.0\nnot really\n%%EOF\n"),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Text("application/pdf", doc); !errors.Is(err, ErrMalformed) {
				t.Errorf("err = %v, want ErrMalformed", err)
			}
		})
	}
}

func TestPageLinesEstimatesUnusableWidths(t *testing.T) {
	texts := []pdf.Text{
		{FontSize: 10, X: 0, Y: 100, W: 0, S: "a"},
		{FontSize: 10, X: 5, Y: 100, W: 0, S: "b"},
		{FontSize: 10, X: 20, Y: 100, W: 0, S: "c"},
		{FontSize: 0, X: 0, Y: 50, W: 0, S: "z"},
		{FontSize: 10, X: 0, Y: 30, W: 0, S: "\n"},
	}
	if got := pageLines(texts); !slicesEqual(got, []string{"ab c", "z"}) {
		t.Errorf("pageLines = %q", got)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
