package extracttest

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
)

const courierWidths = "/FirstChar 32 /LastChar 126 /Widths [600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600 600]"

func Assemble(objects ...string) []byte {
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

func Stream(content string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
}

func PDF(widths bool, pages ...string) []byte {
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
