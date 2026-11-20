package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

type Secret struct {
	value string
}

func NewSecret(value string) Secret { return Secret{value: value} }

func (s Secret) Reveal() string { return s.value }

func (s Secret) IsSet() bool { return s.value != "" }

func (s Secret) String() string {
	if s.value == "" {
		return ""
	}
	return redacted
}

func (s Secret) GoString() string { return fmt.Sprintf("config.Secret(%q)", s.String()) }

func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = fmt.Fprint(f, s.GoString())
		return
	}
	_, _ = fmt.Fprint(f, s.String())
}

func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
