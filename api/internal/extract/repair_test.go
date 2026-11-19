package extract

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
			_, err := ReadPDF(t.Context(), []byte(doc), Limits{})
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

func pagesWithUnreadable(total int, unreadable ...int) []byte {
	pages := make([]string, total)
	for i := range pages {
		pages[i] = agendaPage
	}
	for _, i := range unreadable {
		pages[i] = "1 begin"
	}
	return extracttest.PDF(true, pages...)
}

func TestUnreadablePagesAreSkippedUpToTheShare(t *testing.T) {
	got, err := inProcessText(t, pagesWithUnreadable(10, 0))
	if err != nil || got != strings.TrimSuffix(strings.Repeat("Agenda\n", 9), "\n") {
		t.Errorf("one unreadable page of ten: PDFText = %q, %v", got, err)
	}
	cases := map[string]struct {
		doc  []byte
		want string
	}{
		"one of one":  {pagesWithUnreadable(1, 0), "1 of 1 pages could not be read"},
		"one of nine": {pagesWithUnreadable(9, 4), "1 of 9 pages could not be read, first page 5: "},
		"two of ten":  {pagesWithUnreadable(10, 3, 7), "2 of 10 pages could not be read, first page 4: "},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := inProcessText(t, tc.doc); !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
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
	got, err := ReadPDF(t.Context(), doc, Limits{})
	if err != nil || Normalize(got.Text) != "Agenda" {
		t.Errorf("ReadPDF = %q, %v", got.Text, err)
	}
	r := got.Report
	if r.Pages != 1 || r.TruncatedPages != 1 || r.UnreadablePages != 0 || !strings.HasPrefix(r.FirstError, "page 1: content stream 0: ") || !r.Partial() {
		t.Errorf("Report = %+v", r)
	}
}

func TestReadPDFReportsUnreadablePagesAndTheFirstError(t *testing.T) {
	got, err := ReadPDF(t.Context(), pagesWithUnreadable(20, 1, 2), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	r := got.Report
	if r.Pages != 20 || r.UnreadablePages != 2 || r.TruncatedPages != 0 || !strings.HasPrefix(r.FirstError, "page 2: cannot begin") {
		t.Errorf("Report = %+v", r)
	}
	clean, err := ReadPDF(t.Context(), extracttest.PDF(true, agendaPage), Limits{})
	if err != nil || clean.Report.Partial() || clean.Report.FirstError != "" || clean.Report.DecodedContentBytes != int64(len(agendaPage)) {
		t.Errorf("clean Report = %+v, %v", clean.Report, err)
	}
}

func TestTheUnreadableThresholdIsMoreThanTenPercent(t *testing.T) {
	cases := []struct {
		unreadable, pages int
		want              bool
	}{
		{0, 0, false},
		{0, 1, false},
		{1, 1, true},
		{1, 9, true},
		{1, 10, false},
		{2, 10, true},
		{2, 19, true},
		{2, 20, false},
		{3, 20, true},
		{10, 100, false},
		{11, 100, true},
		{44, 443, false},
		{45, 443, true},
	}
	for _, tc := range cases {
		if got := tooManyUnreadable(tc.unreadable, tc.pages); got != tc.want {
			t.Errorf("tooManyUnreadable(%d, %d) = %v", tc.unreadable, tc.pages, got)
		}
	}
}

func TestTruncateUTF8KeepsWholeRunes(t *testing.T) {
	if got := truncateUTF8("abécd", 3); got != "ab" {
		t.Errorf("truncateUTF8 = %q", got)
	}
	if got := truncateUTF8("abc", 3); got != "abc" {
		t.Errorf("truncateUTF8 = %q", got)
	}
}

func TestTJDoesNotDecodeItsLineBreak(t *testing.T) {
	doc := extracttest.NarrowCodespaceTrueType("BT /F1 12 Tf 72 720 Td [(Agen)-20(da)] TJ ET")
	got, err := ReadPDF(t.Context(), doc, Limits{})
	if err != nil || got.Text != "Agenda\n" || strings.ContainsRune(got.Text, '\ufffd') {
		t.Errorf("ReadPDF = %q, %v", got.Text, err)
	}
}

func TestFontsAreDecodedOncePerDocument(t *testing.T) {
	doc := extracttest.NarrowCodespaceTrueType(
		"BT /F1 12 Tf 72 720 Td (Agenda) Tj ET",
		"BT /F1 12 Tf 72 720 Td (minutes) Tj /F1 10 Tf (item) Tj ET",
		"BT /F1 12 Tf 72 720 Td (adjourn) Tj ET",
	)
	r, err := pdf.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	pages, err := pageTree(r, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := newDocumentReader(DefaultLimits.MaxGlyphsPerPage)
	for _, p := range pages {
		if _, err := d.pageText(p); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.fonts) != 1 {
		t.Errorf("decoded %d fonts, want 1 shared by every page", len(d.fonts))
	}
}

func TestPDFTextBoundsDecodedDocumentContent(t *testing.T) {
	stream, err := extracttest.FlateStream([]byte(agendaPage + "\n" + strings.Repeat(" ", maxContentBytes-len(agendaPage)-1)))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = ReadPDF(t.Context(), extracttest.SharedContents(2000, stream), Limits{})
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "document content exceeds") || !strings.HasSuffix(err.Error(), "on page 5") {
		t.Errorf("err = %v, want the document content limit on page 5", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s", elapsed)
	}
}

func TestThePartSeparatorDoesNotCountTowardThePageLimit(t *testing.T) {
	full, err := extracttest.FlateStream([]byte(agendaPage + "\n" + strings.Repeat(" ", maxContentBytes-len(agendaPage)-1)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadPDF(t.Context(), extracttest.PageOfStreams(full, extracttest.Stream(""), extracttest.Stream("")), Limits{})
	if err != nil || Normalize(got.Text) != "Agenda" || got.Report.DecodedContentBytes != maxContentBytes {
		t.Errorf("ReadPDF = %q, %+v, %v", got.Text, got.Report, err)
	}
}

func TestPDFTextCutsEverythingAfterTheLastEOF(t *testing.T) {
	cases := map[string]struct {
		tail     string
		reported bool
	}{
		"99 spaces":           {strings.Repeat(" ", 99), false},
		"200 spaces":          {strings.Repeat(" ", 200), false},
		"2000 newlines":       {strings.Repeat("\n", 2000), false},
		"2000 NULs":           {strings.Repeat("\x00", 2000), false},
		"60000 NULs":          {strings.Repeat("\x00", 60000), false},
		"mixed padding":       {"\r\n\t\f \x00\x00", false},
		"no padding":          {"", false},
		"second %%EOF":        {"\n%%EOF\n", false},
		"html and crlf":       {"\r\n<html><body>appended</body></html>\r\n", true},
		"truncated update":    {"4 0 obj\n<< /Type /Page /Contents 9 0 R >>\nendobj\nxref\n0 1\n", true},
		"one byte after NULs": {strings.Repeat("\x00", 500) + "x", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			doc := append(extracttest.PDF(true, agendaPage), tc.tail...)
			got, err := ReadPDF(t.Context(), doc, Limits{})
			if err != nil || Normalize(got.Text) != "Agenda" {
				t.Fatalf("ReadPDF = %q, %v", got.Text, err)
			}
			want := 0
			if tc.reported {
				want = len("\n") + len(tc.tail)
			}
			if got.Report.TrailingBytesCut != want || got.Report.Partial() != tc.reported {
				t.Errorf("Report = %+v, want %d trailing bytes cut", got.Report, want)
			}
		})
	}
	if _, err := inProcessText(t, append(extracttest.PDF(true, agendaPage), strings.Repeat("\x00", endOfFileWindow)...)); !errors.Is(err, ErrMalformed) {
		t.Errorf("padding past the window: err = %v, want ErrMalformed", err)
	}
}

func TestPDFTextBoundsDecodedPageContent(t *testing.T) {
	_, err := ReadPDF(t.Context(), flateBombPDF(t, maxContentBytes+1<<20), Limits{MaxGlyphsPerPage: 1 << 30})
	if !errors.Is(err, ErrLimitExceeded) || !strings.Contains(err.Error(), "page content exceeds") {
		t.Errorf("err = %v, want the page content limit", err)
	}
}

func TestResourcesAreInheritedWithinABoundedWalk(t *testing.T) {
	loop := extracttest.Assemble(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 3 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
		extracttest.Stream(agendaPage),
	)
	done := make(chan error, 1)
	go func() {
		_, err := ReadPDF(t.Context(), loop, Limits{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("parent loop: err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a page whose /Parent is itself did not finish")
	}
	inherited := extracttest.Assemble(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /Resources << /Font << /F1 5 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
		extracttest.Stream("BT /F1 12 Tf 72 720 Td (Agenda item 7?) Tj ET"),
		"<< /Type /Font /Subtype /TrueType /BaseFont /Arial /ToUnicode 6 0 R >>",
		extracttest.Stream("1 begincodespacerange <6d> <7a> endcodespacerange 1 beginbfrange <20> <7a> <0020> endbfrange"),
	)
	if got, err := inProcessText(t, inherited); err != nil || got != "Agenda item 7?" {
		t.Errorf("inherited resources: PDFText = %q, %v", got, err)
	}
}
