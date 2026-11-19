package extracttest

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
)

const courierWidths = "/FirstChar 32 /LastChar 126 /Widths [600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600]"

type Framing struct {
	Header    string
	StartXref string
}

var StandardFraming = Framing{Header: "%PDF-1.4\n", StartXref: "startxref\n"}

func Assemble(objects ...string) []byte {
	return StandardFraming.Assemble(objects...)
}

func (f Framing) Assemble(objects ...string) []byte {
	var b bytes.Buffer
	b.WriteString(f.Header)
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
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\n%s%d\n%%%%EOF\n", len(objects)+1, f.StartXref, xref)
	return b.Bytes()
}

func Stream(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

func (f Framing) PDF(pages ...string) []byte {
	return f.Assemble(pdfObjects(true, pages...)...)
}

func PDF(widths bool, pages ...string) []byte {
	return Assemble(pdfObjects(widths, pages...)...)
}

func pdfObjects(widths bool, pages ...string) []string {
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>"
	if widths {
		font = "<< /Type /Font /Subtype /Type1 /BaseFont /Courier " + courierWidths + " >>"
	}
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", font}
	kids := make([]string, 0, len(pages))
	for _, content := range pages {
		pageNum := len(objects) + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageNum))
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", pageNum+1),
			Stream(content))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))
	return objects
}

func SplitContents(parts ...string) []byte {
	streams := make([]string, len(parts))
	for i, part := range parts {
		streams[i] = Stream(part)
	}
	return PageOfStreams(streams...)
}

func PageOfStreams(streams ...string) []byte {
	refs := make([]string, len(streams))
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Courier " + courierWidths + " >>",
	}
	for i, stream := range streams {
		refs[i] = fmt.Sprintf("%d 0 R", len(objects)+1)
		objects = append(objects, stream)
	}
	objects[2] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents [%s] >>", strings.Join(refs, " "))
	return Assemble(objects...)
}

func FlateStream(content []byte) (string, error) {
	var packed bytes.Buffer
	w, err := zlib.NewWriterLevel(&packed, zlib.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(content); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", packed.Len(), packed.Bytes()), nil
}

func SharedContents(pages int, stream string) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Courier " + courierWidths + " >>",
		stream,
	}
	kids := make([]string, pages)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", len(objects)+1)
		objects = append(objects, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 4 0 R >>")
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages)
	return Assemble(objects...)
}

func NarrowCodespaceTrueType(pages ...string) []byte {
	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n" +
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n" +
		"/CMapName /Adobe-Identity-UCS def\n" +
		"1 begincodespacerange <6d> <7a> endcodespacerange\n" +
		"2 beginbfchar\n<41> <0041>\n<5b> <005b>\nendbfchar\n" +
		"2 beginbfrange\n<20> <3f> <0020>\n<61> <7a> [<0061> <0062> <0063> <0064> <0065> <0066> <0067> <0068> <0069> <006a> <006b> <006c> <006d> <006e> <006f> <0070> <0071> <0072> <0073> <0074> <0075> <0076> <0077> <0078> <0079> <007a>]\nendbfrange\n" +
		"endcmap CMapName currentdict /CMap defineresource pop end end\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"",
		"<< /Type /Font /Subtype /TrueType /BaseFont /AAAAAA+Arial " + courierWidths + " /ToUnicode 4 0 R >>",
		Stream(cmap),
	}
	kids := make([]string, len(pages))
	for i, content := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", len(objects)+1)
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", len(objects)+2),
			Stream(content))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))
	return Assemble(objects...)
}

func Lines(lines ...string) []byte {
	var content strings.Builder
	for i, line := range lines {
		escaped := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(line)
		fmt.Fprintf(&content, "BT /F1 10 Tf 72 %d Td (%s) Tj ET\n", 740-14*i, escaped)
	}
	return PDF(true, content.String())
}

func SelfReferencingPages() []byte {
	return Assemble(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [2 0 R 2 0 R] /Count 1 >>",
	)
}

func HugeCount() []byte {
	return Assemble(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 2000000000 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
		Stream("BT /F1 12 Tf 72 720 Td (Agenda) Tj ET"),
	)
}

func FlateBomb(decompressed int) ([]byte, error) {
	var packed bytes.Buffer
	w, err := zlib.NewWriterLevel(&packed, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	line := []byte("BT /F1 1 Tf 0 0 Td (" + strings.Repeat("A", 4000) + ") Tj ET\n")
	for written := 0; written < decompressed; written += len(line) {
		if _, err := w.Write(line); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	content := fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", packed.Len(), packed.Bytes())
	return Assemble(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		content,
		"<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>",
	), nil
}
