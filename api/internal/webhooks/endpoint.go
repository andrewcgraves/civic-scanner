package webhooks

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxEndpointBytes = 2048

type EndpointError struct {
	Code    string
	Message string
}

func (e *EndpointError) Error() string { return e.Message }

func endpointError(code, message string) *EndpointError {
	return &EndpointError{Code: code, Message: message}
}

func NormalizeEndpoint(raw string) (string, *EndpointError) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", endpointError("required", "enter the URL to send webhooks to")
	case len(raw) > MaxEndpointBytes:
		return "", endpointError("too_long", "the URL must be at most 2,048 bytes")
	case !utf8.ValidString(raw) || strings.ContainsRune(raw, utf8.RuneError) || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return "", endpointError("invalid_url", "the URL must not contain spaces or control characters")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "", endpointError("invalid_url", "enter a full URL, such as https://example.com/hooks")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", endpointError("unsupported_scheme", "the URL must start with https://")
	}
	switch {
	case u.Opaque != "" || u.Host == "" || u.Hostname() == "":
		return "", endpointError("invalid_url", "enter a full URL, such as https://example.com/hooks")
	case u.User != nil:
		return "", endpointError("credentials", "the URL must not contain a user name or password")
	case strings.Contains(raw, "#"):
		return "", endpointError("fragment", "the URL must not contain a fragment (#)")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", endpointError("invalid_url", "the port must be a number from 1 to 65535")
		}
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	normalized := u.String()
	if len(normalized) > MaxEndpointBytes {
		return "", endpointError("too_long", "the URL must be at most 2,048 bytes")
	}
	return normalized, nil
}
