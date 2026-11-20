package submission

import (
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

const (
	MinNameRunes    = 2
	MaxNameRunes    = 200
	MaxWebsiteBytes = 2048
	maxHostBytes    = 253
	maxLabelBytes   = 63
)

const (
	FieldName    = "name"
	FieldWebsite = "website"
)

type FieldError struct {
	Field   string
	Code    string
	Message string
}

type Submission struct {
	Name       string
	Website    string
	WebsiteKey string
}

type Website struct {
	URL string
	Key string
}

func Validate(name, website string) (Submission, []FieldError) {
	var problems []FieldError
	cleanName, err := NormalizeName(name)
	if err != nil {
		problems = append(problems, *err)
	}
	site, err := NormalizeWebsite(website)
	if err != nil {
		problems = append(problems, *err)
	}
	if len(problems) > 0 {
		return Submission{}, problems
	}
	return Submission{Name: cleanName, Website: site.URL, WebsiteKey: site.Key}, nil
}

func nameError(code, message string) *FieldError {
	return &FieldError{Field: FieldName, Code: code, Message: message}
}

func websiteError(code, message string) *FieldError {
	return &FieldError{Field: FieldWebsite, Code: code, Message: message}
}

func NormalizeName(raw string) (string, *FieldError) {
	if !utf8.ValidString(raw) {
		return "", nameError("invalid_characters", "Name contains characters that are not valid text.")
	}
	var b strings.Builder
	pendingSpace := false
	hasAlnum := false
	for _, r := range raw {
		switch {
		case unicode.IsSpace(r):
			pendingSpace = b.Len() > 0
			continue
		case !unicode.IsPrint(r):
			return "", nameError("invalid_characters", "Name must be plain text, without control or invisible characters.")
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasAlnum = true
		}
		b.WriteRune(r)
	}
	name := b.String()
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		return "", nameError("required", "Enter the agency's name.")
	case n < MinNameRunes:
		return "", nameError("too_short", "Name must be at least 2 characters.")
	case n > MaxNameRunes:
		return "", nameError("too_long", "Name must be at most 200 characters.")
	case !hasAlnum:
		return "", nameError("invalid_characters", "Name must contain a letter or a digit.")
	}
	return name, nil
}

var defaultPorts = map[string]string{"http": "80", "https": "443"}

var schemeLike = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*):(.?)`)

var nonPublicSuffixes = []string{
	"localhost", "local", "localdomain", "internal", "intranet", "lan", "home", "corp", "private",
	"test", "example", "invalid", "onion", "arpa", "alt",
}

func NormalizeWebsite(raw string) (Website, *FieldError) {
	raw = strings.TrimFunc(raw, unicode.IsSpace)
	switch {
	case raw == "":
		return Website{}, websiteError("required", "Enter the agency's website.")
	case len(raw) > MaxWebsiteBytes:
		return Website{}, websiteError("too_long", "Website must be at most 2,048 characters.")
	case !utf8.ValidString(raw) || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return Website{}, websiteError("invalid_url", "Website must be a web address without spaces.")
	}
	if !strings.Contains(raw, "://") {
		if m := schemeLike.FindStringSubmatch(raw); m != nil && !strings.Contains(m[1], ".") && !isDigit(m[2]) {
			return Website{}, websiteError("unsupported_scheme", "Website must start with http:// or https://.")
		}
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Website{}, websiteError("invalid_url", "Website is not a valid web address.")
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme != "http" && scheme != "https":
		return Website{}, websiteError("unsupported_scheme", "Website must start with http:// or https://.")
	case u.User != nil:
		return Website{}, websiteError("credentials", "Website must not contain a user name or password.")
	case strings.HasPrefix(u.Host, "["):
		return Website{}, websiteError("ip_address", "Website must use a host name, not an IP address.")
	}
	host, problem := normalizeHost(u.Hostname())
	if problem != nil {
		return Website{}, problem
	}
	if port := u.Port(); port != "" && port != defaultPorts[scheme] {
		return Website{}, websiteError("port", "Website must not name a port.")
	}
	site := scheme + "://" + host + cleanPath(u.EscapedPath())
	if len(site) > MaxWebsiteBytes {
		return Website{}, websiteError("too_long", "Website must be at most 2,048 characters.")
	}
	return Website{URL: site, Key: siteKey(host)}, nil
}

func isDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

func normalizeHost(host string) (string, *FieldError) {
	if host == "" {
		return "", websiteError("invalid_url", "Website must include a host name.")
	}
	if net.ParseIP(host) != nil {
		return "", websiteError("ip_address", "Website must use a host name, not an IP address.")
	}
	if strings.Contains(host, "_") {
		return "", websiteError("invalid_host", "Website's host name is not valid.")
	}
	ascii, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
	if err != nil || ascii == "" {
		return "", websiteError("invalid_host", "Website's host name is not valid.")
	}
	labels := strings.Split(ascii, ".")
	tld := labels[len(labels)-1]
	for _, suffix := range nonPublicSuffixes {
		if tld == suffix {
			return "", websiteError("not_public", "Website must be a public site on the internet.")
		}
	}
	if allDigits(tld) || strings.HasPrefix(tld, "0x") {
		return "", websiteError("ip_address", "Website must use a host name, not an IP address.")
	}
	if len(ascii) > maxHostBytes || len(labels) < 2 || !validTLD(tld) {
		return "", websiteError("invalid_host", "Website's host name is not valid.")
	}
	for _, label := range labels {
		if !validLabel(label) {
			return "", websiteError("invalid_host", "Website's host name is not valid.")
		}
	}
	return ascii, nil
}

func allDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func validTLD(tld string) bool {
	if strings.HasPrefix(tld, "xn--") {
		return true
	}
	return len(tld) >= 2 && strings.Trim(tld, "abcdefghijklmnopqrstuvwxyz") == ""
}

func validLabel(label string) bool {
	if label == "" || len(label) > maxLabelBytes || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	return strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") == ""
}

func cleanPath(escaped string) string {
	if escaped == "" {
		return ""
	}
	cleaned := path.Clean("/" + escaped)
	if cleaned == "/" {
		return ""
	}
	return cleaned
}

func siteKey(host string) string {
	if rest, ok := strings.CutPrefix(host, "www."); ok && strings.Contains(rest, ".") {
		return rest
	}
	return host
}
