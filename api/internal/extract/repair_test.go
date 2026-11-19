package extract

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract/extracttest"
)

const agendaPage = "BT /F1 12 Tf 72 720 Td (Agenda) Tj ET"

func TestLegistarFramingIsRejectedByTheLibraryAlone(t *testing.T) {
	doc := extracttest.Framing{Header: "%PDF-1.7 \n", StartXref: "startxref \n"}.PDF(agendaPage)
	if _, err := pdf.NewReader(bytes.NewReader(doc), int64(len(doc))); err == nil {
		t.Fatal("the library accepted a header with a space before the newline; this regression fixture no longer reproduces")
	}
	fixed := extracttest.Framing{Header: "%PDF-1.7\n\n", StartXref: "startxref \n"}.PDF(agendaPage)
	if _, err := pdf.NewReader(bytes.NewReader(fixed), int64(len(fixed))); err == nil || !strings.Contains(err.Error(), "startxref") {
		t.Fatalf("library with a clean header and a space after startxref: err = %v, want a startxref error", err)
	}
}

func TestPDFTextAcceptsHeaderAndTrailerVariants(t *testing.T) {
	cases := map[string][]byte{
		"legistar":            extracttest.Framing{Header: "%PDF-1.7 \n", StartXref: "startxref \n"}.PDF(agendaPage),
		"crlf":                extracttest.Framing{Header: "%PDF-1.7\r\n", StartXref: "startxref\r\n"}.PDF(agendaPage),
		"pdf 2.0":             extracttest.Framing{Header: "%PDF-2.0\n", StartXref: "startxref\n"}.PDF(agendaPage),
		"tabs":                extracttest.Framing{Header: "%PDF-1.5\t\n", StartXref: "\tstartxref\t\n"}.PDF(agendaPage),
		"comment after":       extracttest.Framing{Header: "%PDF-1.6%\xe2\xe3\xcf\xd3\n", StartXref: "startxref\n"}.PDF(agendaPage),
		"junk before":         append([]byte("HTTP junk\r\n\r\n"), extracttest.PDF(true, agendaPage)...),
		"junk after %%EOF":    append(extracttest.PDF(true, agendaPage), []byte("\x00\x00\x00<html>trailing</html>")...),
		"whitespace after":    append(extracttest.PDF(true, agendaPage), []byte("\r\n\r\n  ")...),
		"legistar after junk": append([]byte("\xef\xbb\xbf"), extracttest.Framing{Header: "%PDF-1.7 \n", StartXref: "startxref \n"}.PDF(agendaPage)...),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := inProcessText(t, doc)
			if err != nil || got != "Agenda" {
				t.Errorf("PDFText = %q, %v", got, err)
			}
		})
	}
}

func TestPDFTextRejectsWhatIsNotAPDFHeader(t *testing.T) {
	body := string(extracttest.PDF(true, agendaPage)[len("%PDF-1.4\n"):])
	cases := map[string]string{
		"no header":         "<html><body>Agenda</body></html>",
		"empty":             "",
		"too short":         "%PDF-1",
		"letters version":   "%PDF-x.y\n" + body,
		"version 3":         "%PDF-3.0\n" + body,
		"no dot":            "%PDF-1-7\n" + body,
		"text after":        "%PDF-1.7abc\n" + body,
		"header past 1 KiB": strings.Repeat(" ", pdfHeaderWindow) + "%PDF-1.4\n" + body,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := PDFText(t.Context(), []byte(doc), Limits{})
			if !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "invalid header") {
				t.Errorf("err = %v, want ErrMalformed for an invalid header", err)
			}
		})
	}
}

func TestPatchedDocumentReadsAcrossPatches(t *testing.T) {
	doc := patchedDocument{body: []byte("abcdef"), patches: map[int64]byte{1: 'X', 4: 'Y'}}
	buf := make([]byte, 3)
	n, err := doc.ReadAt(buf, 3)
	if n != 3 || err != nil || string(buf) != "dYf" {
		t.Errorf("ReadAt(3) = %d, %v, %q", n, err, buf)
	}
	n, err = doc.ReadAt(buf, 5)
	if n != 1 || err == nil || buf[0] != 'f' {
		t.Errorf("ReadAt(5) = %d, %v, %q", n, err, buf[:n])
	}
}

func libraryContentPanics(t *testing.T, doc []byte) bool {
	t.Helper()
	r, err := pdf.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		r.Page(1).Content()
	}()
	return panicked
}

func TestPDFTextReadsContentSplitAcrossStreams(t *testing.T) {
	doc := extracttest.SplitContents(
		"q",
		"0.75 0 0 0.75 0 0 cm BT /F1 16 Tf 96 960 Td (Agenda item one) Tj ET",
		"Q",
		"q BT /F1 12 Tf 72 700 Td [(Consent)-400(Calendar)]",
		"TJ ET Q",
		"q BT /F1 10 Tf 72 40 Td (Page 1) Tj ET Q",
	)
	if !libraryContentPanics(t, doc) {
		t.Fatal("the library read this page without panicking; this regression fixture no longer reproduces")
	}
	got, err := inProcessText(t, doc)
	if want := "Agenda item one\nConsent Calendar\nPage 1"; err != nil || got != want {
		t.Errorf("PDFText = %q, %v, want %q", got, err, want)
	}
}

func TestPDFTextToleratesUnbalancedStateAndBadOperands(t *testing.T) {
	doc := extracttest.PDF(true, "Q Q 1 0 0 cm BT /F1 Tf /F1 12 Tf 72 720 Td Tc 1 2 Tm Tz TL Ts (Agenda) Tj ' \" ET Q")
	if !libraryContentPanics(t, doc) {
		t.Fatal("the library read this page without panicking; this regression fixture no longer reproduces")
	}
	got, err := inProcessText(t, doc)
	if err != nil || got != "Agenda" {
		t.Errorf("PDFText = %q, %v", got, err)
	}
}

func TestPDFTextAppliesTextStateOperators(t *testing.T) {
	doc := extracttest.PDF(true, "BT /F1 10 Tf 14 TL 100 Tz 0 Ts 0 Tc 1 0 0 1 72 720 Tm (Call to Order) Tj T* (Roll Call) Tj (Public Comment) ' 0 0 (Consent Calendar) \" 0 -14 TD (Adjourn) Tj ET")
	got, err := inProcessText(t, doc)
	if want := "Call to Order\nRoll Call\nPublic Comment\nConsent Calendar\nAdjourn"; err != nil || got != want {
		t.Errorf("PDFText = %q, %v, want %q", got, err, want)
	}
}

func TestPDFTextDecodesSimpleFontsOutsideTheirCodespace(t *testing.T) {
	doc := extracttest.NarrowCodespaceTrueType("BT /F1 12 Tf 72 720 Td (Agenda item 7?) Tj ET")
	r, err := pdf.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	var library strings.Builder
	for _, g := range r.Page(1).Content().Text {
		library.WriteString(g.S)
	}
	if strings.Contains(library.String(), "Agenda") {
		t.Fatalf("the library decoded %q; this regression fixture no longer reproduces", library.String())
	}
	got, err := inProcessText(t, doc)
	if err != nil || got != "Agenda item 7?" {
		t.Errorf("PDFText = %q, %v", got, err)
	}
}

func TestSimpleFontEncodingReadsBfcharAndBfrange(t *testing.T) {
	e := &simpleFontEncoding{fallback: identityEncoding{}}
	e.read([]byte("2 beginbfchar <01> <0041> <0002> <00420043> endbfchar " +
		"3 beginbfrange <10> <12> <00FF> <20> <21> [<0061> <0062>] <30> <30> <41> endbfrange " +
		"1 beginbfchar <40> (x) endbfchar " +
		"1 beginbfchar <41> <zz> endbfchar " +
		"1 beginbfchar <42> <0058 endbfchar " +
		"1 beginbfchar <0102> <0059> endbfchar " +
		"1 beginbfrange <50> <51> [<0070> endbfrange " +
		"beginbfchar <60>"))
	if got, want := e.Decode("\x01\x02\x10\x11\x12\x20\x21\x30\x40\x41\x42\x50\x7a"), "ABCÿĀāabA@ABpz"; got != want {
		t.Errorf("Decode = %q, want %q", got, want)
	}
	if _, ok := singleByteCode(cmapToken{bracket: '['}); ok {
		t.Error("a bracket is not a code")
	}
}

func TestUnreadablePagesAreSkippedUnlessMostAre(t *testing.T) {
	doc := extracttest.PDF(true, "1 begin", agendaPage, "BT /F1 12 Tf 72 720 Td (Minutes) Tj ET")
	got, err := inProcessText(t, doc)
	if err != nil || got != "Agenda\nMinutes" {
		t.Errorf("one unreadable page of three: PDFText = %q, %v", got, err)
	}
	doc = extracttest.PDF(true, "1 begin", "2 begin", agendaPage)
	if _, err := inProcessText(t, doc); !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "2 of 3 pages could not be read") {
		t.Errorf("two unreadable pages of three: err = %v", err)
	}
}

func TestPDFTextKeepsTheDecodedPartOfATruncatedStream(t *testing.T) {
	var packed bytes.Buffer
	w := zlib.NewWriter(&packed)
	_, _ = w.Write([]byte(agendaPage + "\n"))
	_ = w.Close()
	truncated := packed.Bytes()[:packed.Len()-4]
	doc := extracttest.Assemble(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(truncated), truncated),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>",
	)
	got, err := inProcessText(t, doc)
	if err != nil || got != "Agenda" {
		t.Errorf("PDFText = %q, %v", got, err)
	}
}

func TestPDFTextBoundsDecodedPageContent(t *testing.T) {
	_, err := PDFText(t.Context(), flateBombPDF(t, maxContentBytes+1<<20), Limits{MaxGlyphsPerPage: 1 << 30})
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "page content exceeds") {
		t.Errorf("err = %v, want the page content limit", err)
	}
}
