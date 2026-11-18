package extract

import (
	"errors"
	"strings"
	"testing"
)

func TestHTMLTextKeepsVisibleTextInBlocks(t *testing.T) {
	page := `<!DOCTYPE html>
<html><head><title>Ignored title</title><style>p{color:red}</style></head>
<body>
<nav>Home</nav>
<h1>Regular Board Meeting</h1>
<p>Thursday, <b>December&nbsp;4</b>, 2026<br>10:00 AM</p>
<script>alert("x")</script>
<!-- a comment -->
<ul><li>Call to Order</li><li>Roll Call</li></ul>
<table><tr><th>Item</th><th>Title</th></tr><tr><td>5.</td><td>Approve Consent Calendar</td></tr></table>
<noscript>Enable JavaScript</noscript>
</body></html>`
	got, err := extractor().Text(t.Context(), Request{ContentType: "text/html; charset=utf-8", Body: []byte(page)})
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	want := "Home\nRegular Board Meeting\nThursday, December 4, 2026\n10:00 AM\nCall to Order\nRoll Call\nItem Title\n5. Approve Consent Calendar"
	if got != want {
		t.Errorf("Text =\n%s\nwant\n%s", got, want)
	}
}

func TestHTMLTextDecodesTheDeclaredCharset(t *testing.T) {
	cases := map[string][]byte{
		"text/html; charset=iso-8859-1": []byte("<html><body><p>Caf\xe9 Agenda</p></body></html>"),
		"text/html":                     []byte(`<html><head><meta charset="windows-1252"></head><body><p>Caf` + "\xe9" + ` Agenda</p></body></html>`),
	}
	for ct, page := range cases {
		got, err := extractor().Text(t.Context(), Request{ContentType: ct, Body: page})
		if err != nil || got != "Café Agenda" {
			t.Errorf("Text(%q) = %q, %v", ct, got, err)
		}
	}
}

func TestHTMLTooDeeplyNestedIsMalformed(t *testing.T) {
	page := strings.Repeat("<div>", 600) + "Agenda" + strings.Repeat("</div>", 600)
	if _, err := extractor().Text(t.Context(), Request{ContentType: "text/html", Body: []byte(page)}); !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed", err)
	}
}
