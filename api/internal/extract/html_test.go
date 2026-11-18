package extract

import "testing"

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
	got, err := Text("text/html; charset=utf-8", []byte(page))
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	want := "Home\nRegular Board Meeting\nThursday, December 4, 2026\n10:00 AM\nCall to Order\nRoll Call\nItem Title\n5. Approve Consent Calendar"
	if got != want {
		t.Errorf("Text =\n%s\nwant\n%s", got, want)
	}
}

func TestHTMLTextDecodesTheDeclaredCharset(t *testing.T) {
	page := []byte("<html><body><p>Caf\xe9 Agenda</p></body></html>")
	got, err := Text("text/html; charset=iso-8859-1", page)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if got != "Café Agenda" {
		t.Errorf("Text = %q", got)
	}
}
