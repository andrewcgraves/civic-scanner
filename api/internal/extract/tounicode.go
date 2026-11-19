package extract

import (
	"bytes"
	"encoding/hex"
	"io"
	"strings"
	"unicode/utf16"

	"github.com/ledongthuc/pdf"
)

const maxToUnicodeBytes = 1 << 20

type simpleFontEncoding struct {
	codes    [256]string
	mapped   [256]bool
	fallback pdf.TextEncoding
}

func (e *simpleFontEncoding) Decode(raw string) string {
	var b strings.Builder
	for i := range len(raw) {
		c := raw[i]
		if e.mapped[c] {
			b.WriteString(e.codes[c])
			continue
		}
		b.WriteString(e.fallback.Decode(raw[i : i+1]))
	}
	return b.String()
}

func fontEncoding(font *pdf.Font) pdf.TextEncoding {
	var fallback pdf.TextEncoding = identityEncoding{}
	if enc := font.Encoder(); enc != nil {
		fallback = enc
	}
	toUnicode := font.V.Key("ToUnicode")
	if font.V.Key("Subtype").Name() == "Type0" || font.V.Key("Encoding").Kind() != pdf.Null || toUnicode.Kind() != pdf.Stream {
		return fallback
	}
	rc := toUnicode.Reader()
	data, _ := io.ReadAll(io.LimitReader(rc, maxToUnicodeBytes))
	_ = rc.Close()
	e := &simpleFontEncoding{fallback: fallback}
	e.read(data)
	return e
}

type cmapToken struct {
	bracket byte
	value   string
}

func (e *simpleFontEncoding) read(data []byte) {
	for _, section := range sections(data, "beginbfchar", "endbfchar") {
		tokens, ok := cmapTokens(section)
		for i := 0; ok && i+1 < len(tokens); i += 2 {
			if code, isCode := singleByteCode(tokens[i]); isCode && tokens[i+1].bracket == 0 {
				e.set(code, tokens[i+1].value)
			}
		}
	}
	for _, section := range sections(data, "beginbfrange", "endbfrange") {
		tokens, ok := cmapTokens(section)
		for i := 0; ok && i+2 < len(tokens); {
			lo, okLo := singleByteCode(tokens[i])
			hi, okHi := singleByteCode(tokens[i+1])
			if tokens[i+2].bracket == '[' {
				end := i + 3
				for end < len(tokens) && tokens[end].bracket != ']' {
					end++
				}
				for j, dst := range tokens[i+3 : end] {
					if okLo && okHi && int(lo)+j <= int(hi) {
						e.set(lo+byte(j), dst.value)
					}
				}
				i = end + 1
				continue
			}
			if okLo && okHi && lo <= hi && tokens[i+2].bracket == 0 {
				dst := []byte(tokens[i+2].value)
				for c := lo; ; c++ {
					e.set(c, string(dst))
					increment(dst)
					if c == hi {
						break
					}
				}
			}
			i += 3
		}
	}
}

func (e *simpleFontEncoding) set(code byte, dst string) {
	e.codes[code], e.mapped[code] = decodeUTF16(dst), true
}

func sections(data []byte, begin, end string) [][]byte {
	var out [][]byte
	for {
		i := bytes.Index(data, []byte(begin))
		if i < 0 {
			return out
		}
		data = data[i+len(begin):]
		j := bytes.Index(data, []byte(end))
		if j < 0 {
			return out
		}
		out = append(out, data[:j])
		data = data[j+len(end):]
	}
}

func cmapTokens(section []byte) ([]cmapToken, bool) {
	var tokens []cmapToken
	for i := 0; i < len(section); {
		switch c := section[i]; c {
		case '[', ']':
			tokens = append(tokens, cmapToken{bracket: c})
			i++
		case '<':
			j := bytes.IndexByte(section[i:], '>')
			if j < 0 {
				return nil, false
			}
			hexText := strings.Join(strings.Fields(string(section[i+1:i+j])), "")
			if len(hexText)%2 == 1 {
				hexText += "0"
			}
			decoded, err := hex.DecodeString(hexText)
			if err != nil {
				return nil, false
			}
			tokens = append(tokens, cmapToken{value: string(decoded)})
			i += j + 1
		case ' ', '\t', '\r', '\n', '\f', 0:
			i++
		default:
			return nil, false
		}
	}
	return tokens, true
}

func singleByteCode(token cmapToken) (byte, bool) {
	if token.bracket != 0 || token.value == "" {
		return 0, false
	}
	v := token.value
	if strings.Trim(v[:len(v)-1], "\x00") != "" {
		return 0, false
	}
	return v[len(v)-1], true
}

func increment(b []byte) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return
		}
	}
}

func decodeUTF16(s string) string {
	if len(s)%2 == 1 {
		runes := make([]rune, len(s))
		for i := range len(s) {
			runes[i] = rune(s[i])
		}
		return string(runes)
	}
	u := make([]uint16, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		u = append(u, uint16(s[i])<<8|uint16(s[i+1]))
	}
	return string(utf16.Decode(u))
}
