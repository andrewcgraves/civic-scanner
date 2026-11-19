package summarize

import (
	"strings"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
)

func PlainText(s string) string {
	return extract.Normalize(strings.Map(dropFormatting, s))
}
