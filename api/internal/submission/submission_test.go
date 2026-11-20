package submission

import (
	"strings"
	"testing"
)

func TestNormalizeWebsiteSameSite(t *testing.T) {
	tests := []struct {
		raw, url, key string
	}{
		{"https://www.metro.net", "https://www.metro.net", "metro.net"},
		{"metro.net", "https://metro.net", "metro.net"},
		{"www.metro.net", "https://www.metro.net", "metro.net"},
		{"HTTP://WWW.Metro.NET/", "http://www.metro.net", "metro.net"},
		{"  https://metro.net/  ", "https://metro.net", "metro.net"},
		{"https://metro.net:443", "https://metro.net", "metro.net"},
		{"http://metro.net:80/", "http://metro.net", "metro.net"},
		{"https://metro.net./", "https://metro.net", "metro.net"},
		{"https://metro.net/about/board//", "https://metro.net/about/board", "metro.net"},
		{"https://metro.net/a/../b/./c", "https://metro.net/b/c", "metro.net"},
		{"https://metro.net/?utm_source=x#top", "https://metro.net", "metro.net"},
		{"https://metro.net/Board%20Meetings", "https://metro.net/Board%20Meetings", "metro.net"},
		{"https://boards.metro.net", "https://boards.metro.net", "boards.metro.net"},
		{"https://www2.metro.net", "https://www2.metro.net", "www2.metro.net"},
		{"https://www.com", "https://www.com", "www.com"},
		{"https://münchen.de", "https://xn--mnchen-3ya.de", "xn--mnchen-3ya.de"},
		{"https://XN--MNCHEN-3YA.DE", "https://xn--mnchen-3ya.de", "xn--mnchen-3ya.de"},
		{"https://xn--e1afmkfd.xn--p1ai", "https://xn--e1afmkfd.xn--p1ai", "xn--e1afmkfd.xn--p1ai"},
		{"metro.net:443/board", "https://metro.net/board", "metro.net"},
		{"https://1.2.3.4.nip.io", "https://1.2.3.4.nip.io", "1.2.3.4.nip.io"},
	}
	for _, tt := range tests {
		got, problem := NormalizeWebsite(tt.raw)
		if problem != nil {
			t.Errorf("%q: unexpected problem %+v", tt.raw, problem)
			continue
		}
		if got.URL != tt.url || got.Key != tt.key {
			t.Errorf("%q = %+v, want url %q key %q", tt.raw, got, tt.url, tt.key)
		}
	}
}

func TestNormalizeWebsiteRejects(t *testing.T) {
	tests := []struct {
		raw, code string
	}{
		{"", "required"},
		{"   ", "required"},
		{"https://" + strings.Repeat("a", 2040) + ".org", "too_long"},
		{"https://metro.net/" + strings.Repeat("a", 2031), "too_long"},
		{"metro.net/" + strings.Repeat("a", 2038), "too_long"},
		{"https://metro .net", "invalid_url"},
		{"https://metro.net/\x00", "invalid_url"},
		{"https://metro.net/\xff", "invalid_url"},
		{"https://metro.net/" + string(rune(0xFFFD)), "invalid_url"},
		{"https://metro" + string(rune(0xFFFD)) + ".net", "invalid_url"},
		{"https://%zz", "invalid_url"},
		{"https://metro.net:abc", "invalid_url"},
		{"https://", "invalid_url"},
		{"https:///path", "invalid_url"},
		{"javascript:alert(1)", "unsupported_scheme"},
		{"mailto:board@metro.net", "unsupported_scheme"},
		{"data:text/html,hi", "unsupported_scheme"},
		{"http:/metro.net", "unsupported_scheme"},
		{"ftp://metro.net", "unsupported_scheme"},
		{"file:///etc/passwd", "unsupported_scheme"},
		{"https:metro.net", "unsupported_scheme"},
		{"https://user:pass@metro.net", "credentials"},
		{"https://user@metro.net", "credentials"},
		{"https://metro.net:8080", "port"},
		{"http://metro.net:443", "port"},
		{"https://metro.net:80", "port"},
		{"https://127.0.0.1", "ip_address"},
		{"http://10.0.0.1/admin", "ip_address"},
		{"https://[::1]", "ip_address"},
		{"https://[fd00::1]:443", "ip_address"},
		{"https://169.254.169.254/latest/meta-data", "ip_address"},
		{"https://127.1", "ip_address"},
		{"https://2130706433", "ip_address"},
		{"https://0x7f000001", "ip_address"},
		{"https://localhost", "not_public"},
		{"localhost:3000", "not_public"},
		{"https://api.localhost", "not_public"},
		{"https://printer.local", "not_public"},
		{"https://db.internal", "not_public"},
		{"https://site.test", "not_public"},
		{"https://site.example", "not_public"},
		{"https://site.invalid", "not_public"},
		{"https://router.home.arpa", "not_public"},
		{"https://hidden.onion", "not_public"},
		{"https://boardroom", "invalid_host"},
		{"https://metro", "invalid_host"},
		{"https://metro_net.org", "invalid_host"},
		{"https://metro%2enet", "invalid_url"},
		{"https://-metro.net", "invalid_host"},
		{"https://metro-.net", "invalid_host"},
		{"https://metro..net", "invalid_host"},
		{"https://metro.n", "invalid_host"},
		{"https://metro.n3t", "invalid_host"},
		{"https://metro.xn--", "invalid_host"},
		{"https://" + strings.Repeat("a", 64) + ".org", "invalid_host"},
		{"https://" + strings.Repeat("abcdefgh.", 29) + "org", "invalid_host"},
		{"https://metro\u2024net", "invalid_host"},
	}
	for _, tt := range tests {
		got, problem := NormalizeWebsite(tt.raw)
		if problem == nil {
			t.Errorf("%q = %+v, want %s", tt.raw, got, tt.code)
			continue
		}
		if problem.Code != tt.code || problem.Field != FieldWebsite || problem.Message == "" {
			t.Errorf("%q: problem %+v, want code %s", tt.raw, problem, tt.code)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		raw, want string
	}{
		{"LA Metro", "LA Metro"},
		{"  Orange   County\tTransportation\nAuthority  ", "Orange County Transportation Authority"},
		{"Metro\u00a0Link", "Metro Link"},
		{"Autoridad de Tránsito", "Autoridad de Tránsito"},
		{"交通局", "交通局"},
		{"<b>Metro</b> & Co.", "<b>Metro</b> & Co."},
		{"A1", "A1"},
		{strings.Repeat("é", MaxNameRunes), strings.Repeat("é", MaxNameRunes)},
	}
	for _, tt := range tests {
		got, problem := NormalizeName(tt.raw)
		if problem != nil || got != tt.want {
			t.Errorf("%q = %q, %+v, want %q", tt.raw, got, problem, tt.want)
		}
	}
}

func TestNormalizeNameRejects(t *testing.T) {
	tests := []struct {
		raw, code string
	}{
		{"", "required"},
		{" \t\n ", "required"},
		{"A", "too_short"},
		{"  A  ", "too_short"},
		{strings.Repeat("a", MaxNameRunes+1), "too_long"},
		{"---", "invalid_characters"},
		{"Metro\x00", "invalid_characters"},
		{"Metro\x1b[31m", "invalid_characters"},
		{"Metro\u200bLink", "invalid_characters"},
		{"Metro\u202eknil", "invalid_characters"},
		{"Metro\ufeff", "invalid_characters"},
		{"Metro\ue000", "invalid_characters"},
		{"Metro\U000E0041", "invalid_characters"},
		{"Metro\uffff", "invalid_characters"},
		{"Metro\ufdd0", "invalid_characters"},
		{"Metro\xff", "invalid_characters"},
		{"Big " + string(rune(0xFFFD)) + string(rune(0xFFFD)), "invalid_characters"},
	}
	for _, tt := range tests {
		got, problem := NormalizeName(tt.raw)
		if problem == nil {
			t.Errorf("%q = %q, want %s", tt.raw, got, tt.code)
			continue
		}
		if problem.Code != tt.code || problem.Field != FieldName || problem.Message == "" {
			t.Errorf("%q: problem %+v, want code %s", tt.raw, problem, tt.code)
		}
	}
}

func TestValidate(t *testing.T) {
	got, problems := Validate(" LA Metro ", "WWW.METRO.NET/")
	if problems != nil {
		t.Fatalf("problems = %+v", problems)
	}
	if want := (Submission{Name: "LA Metro", Website: "https://www.metro.net", WebsiteKey: "metro.net"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	got, problems = Validate("", "localhost")
	if got != (Submission{}) || len(problems) != 2 || problems[0].Field != FieldName || problems[1].Field != FieldWebsite {
		t.Errorf("got %+v, problems %+v, want one problem per field", got, problems)
	}
	if _, problems := Validate("LA Metro", "ftp://metro.net"); len(problems) != 1 || problems[0].Code != "unsupported_scheme" {
		t.Errorf("problems = %+v", problems)
	}
}
