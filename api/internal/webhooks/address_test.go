package webhooks

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][]string
	err     error
	asked   []string
}

func (f *fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, network+" "+host)
	if f.err != nil {
		return nil, f.err
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("lookup without a deadline")
	}
	answer, ok := f.answers[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	out := make([]netip.Addr, 0, len(answer))
	for _, a := range answer {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func TestAddressCheckAccepts(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]string{
		"hooks.example.com":     {"93.184.216.34"},
		"dual.example.com":      {"93.184.216.34", "2606:4700::6810:84e5"},
		"xn--mnchen-3ya.de":     {"93.184.216.35"},
		"trailing.example.com.": {"93.184.216.36"},
	}}
	check := AddressCheck{Resolver: resolver}
	for _, endpoint := range []string{
		"https://hooks.example.com/in",
		"https://hooks.example.com:8443/in?token=x",
		"https://dual.example.com/",
		"https://xn--mnchen-3ya.de/hook",
		"https://münchen.de/hook",
		"https://trailing.example.com./x",
		"https://93.184.216.34/hook",
		"https://[2606:4700::6810:84e5]/hook",
	} {
		if problem := check.Check(t.Context(), endpoint); problem != nil {
			t.Errorf("Check(%q) = %+v, want accepted", endpoint, problem)
		}
	}
	for _, asked := range resolver.asked {
		if asked == "ip münchen.de" {
			t.Errorf("looked up the Unicode name instead of its ASCII form")
		}
	}
}

func TestAddressCheckRefuses(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]string{
		"internal.example.com": {"10.0.0.5"},
		"metadata.example.com": {"169.254.169.254"},
		"loop.example.com":     {"127.0.0.1"},
		"mixed.example.com":    {"93.184.216.34", "192.168.1.10"},
		"ula.example.com":      {"fd12:3456::1"},
		"mapped.example.com":   {"::ffff:127.0.0.1"},
		"cgnat.example.com":    {"100.64.0.1"},
		"empty.example.com":    {},
	}}
	check := AddressCheck{Resolver: resolver}
	for endpoint, code := range map[string]string{
		"https://127.0.0.1/hook":            "not_public",
		"https://10.1.2.3/hook":             "not_public",
		"https://169.254.169.254/latest":    "not_public",
		"https://[::1]/hook":                "not_public",
		"https://[fe80::1%25eth0]/hook":     "not_public",
		"https://[::ffff:10.0.0.1]/hook":    "not_public",
		"https://0.0.0.0/hook":              "not_public",
		"https://localhost/hook":            "not_public",
		"https://api.localhost/hook":        "not_public",
		"https://2130706433/hook":           "not_public",
		"https://0x7f000001/hook":           "not_public",
		"https://127.1/hook":                "not_public",
		"https://internal.example.com/hook": "not_public",
		"https://metadata.example.com/hook": "not_public",
		"https://loop.example.com/hook":     "not_public",
		"https://mixed.example.com/hook":    "not_public",
		"https://ula.example.com/hook":      "not_public",
		"https://mapped.example.com/hook":   "not_public",
		"https://cgnat.example.com/hook":    "not_public",
		"https://missing.example.com/hook":  "unresolvable_host",
		"https://empty.example.com/hook":    "unresolvable_host",
		"https://xn--a.example.com/hook":    "unresolvable_host",
		"https://x\u200d.example/hook":      "invalid_url",
		"https:///hook":                     "invalid_url",
	} {
		problem := check.Check(t.Context(), endpoint)
		if problem == nil || problem.Code != code || problem.Message == "" {
			t.Errorf("Check(%q) = %+v, want code %s", endpoint, problem, code)
		}
	}
}

func TestAddressCheckDoesNotResolveIPLiterals(t *testing.T) {
	resolver := &fakeResolver{}
	check := AddressCheck{Resolver: resolver}
	for _, endpoint := range []string{"https://93.184.216.34/x", "https://10.0.0.1/x", "https://localhost/x"} {
		_ = check.Check(t.Context(), endpoint)
	}
	if len(resolver.asked) != 0 {
		t.Errorf("asked the resolver %v", resolver.asked)
	}
}

func TestAddressCheckResolverFailure(t *testing.T) {
	check := AddressCheck{Resolver: &fakeResolver{err: context.DeadlineExceeded}}
	if problem := check.Check(t.Context(), "https://hooks.example.com/x"); problem == nil || problem.Code != "unresolvable_host" {
		t.Errorf("Check with a failing resolver = %+v", problem)
	}
}

func TestAddressCheckAllowance(t *testing.T) {
	check := AddressCheck{Resolver: &fakeResolver{answers: map[string][]string{"local.test": {"127.0.0.1"}, "localhost": {"127.0.0.1", "::1"}}}, Allow: netip.Addr.IsLoopback}
	for _, endpoint := range []string{"https://127.0.0.1:8443/x", "https://local.test/x", "https://localhost/x"} {
		if problem := check.Check(t.Context(), endpoint); problem != nil {
			t.Errorf("Check(%q) with a loopback allowance = %+v", endpoint, problem)
		}
	}
	if problem := check.Check(t.Context(), "https://93.184.216.34/x"); problem == nil || problem.Code != "not_public" {
		t.Errorf("a public address outside the allowance = %+v", problem)
	}
}

type slowResolver struct{}

func (slowResolver) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAddressCheckStopsWithTheRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	problem := AddressCheck{Resolver: slowResolver{}}.Check(ctx, "https://slow.example.com/x")
	if problem == nil || problem.Code != "unresolvable_host" || time.Since(start) > resolveTimeout {
		t.Errorf("slow lookup = %+v after %v", problem, time.Since(start))
	}
}
