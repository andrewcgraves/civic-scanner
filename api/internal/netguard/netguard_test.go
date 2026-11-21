package netguard_test

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/netguard"
)

func TestPublicAddress(t *testing.T) {
	cases := map[string]bool{
		"93.184.216.34":          true,
		"2606:4700::6810:84e5":   true,
		"::ffff:93.184.216.34":   true,
		"127.0.0.1":              false,
		"127.8.9.10":             false,
		"::1":                    false,
		"10.1.2.3":               false,
		"172.16.0.1":             false,
		"172.31.255.255":         false,
		"192.168.1.1":            false,
		"169.254.169.254":        false,
		"fe80::1":                false,
		"fe80::1%eth0":           false,
		"fd12:3456::1":           false,
		"fd00:ec2::254":          false,
		"fec0::1":                false,
		"100.64.0.1":             false,
		"100.127.255.255":        false,
		"0.0.0.0":                false,
		"0.1.2.3":                false,
		"::":                     false,
		"224.0.0.1":              false,
		"ff02::1":                false,
		"255.255.255.255":        false,
		"198.18.0.1":             false,
		"::ffff:127.0.0.1":       false,
		"::ffff:10.0.0.1":        false,
		"::ffff:169.254.169.254": false,
		"::ffff:100.64.0.1":      false,
		"::127.0.0.1":            false,
		"64:ff9b::a9fe:a9fe":     false,
		"2002:a9fe:a9fe::1":      false,
		"172.15.255.255":         true,
		"172.32.0.0":             true,
		"100.63.255.255":         true,
		"100.128.0.0":            true,
	}
	for addr, want := range cases {
		if got := netguard.PublicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddress(%s) = %v, want %v", addr, got, want)
		}
	}
	if netguard.PublicAddress(netip.Addr{}) {
		t.Error("PublicAddress(zero) = true")
	}
}

func TestDialerRefusesBeforeConnecting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	for _, host := range []string{"127.0.0.1", "localhost"} {
		_, err := netguard.Dialer{}.DialContext(t.Context(), "tcp", net.JoinHostPort(host, port))
		var refused *netguard.RefusedError
		if !errors.As(err, &refused) || !errors.Is(err, netguard.ErrForbiddenAddress) {
			t.Fatalf("%s: err = %v, want a RefusedError", host, err)
		}
		if refused.Host != host || !strings.Contains(err.Error(), host) {
			t.Errorf("%s: refused = %+v, message %q", host, refused, err)
		}
	}

	conn, err := netguard.Dialer{Allow: netip.Addr.IsLoopback}.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("with an allowance: %v", err)
	}
	_ = conn.Close()
}

func TestCheckHost(t *testing.T) {
	cases := map[string]bool{
		"agency.gov":      true,
		"93.184.216.34":   true,
		"2606:4700::1":    true,
		"example.123abc":  true,
		"localhost":       false,
		"LocalHost.":      false,
		"db.localhost":    false,
		"127.0.0.1":       false,
		"::1":             false,
		"169.254.169.254": false,
		"10.0.0.1":        false,
		"::ffff:10.0.0.1": false,
		"2130706433":      false,
		"0x7f000001":      false,
		"127.1":           false,
		"0177.0.0.1":      false,
	}
	for host, want := range cases {
		err := netguard.Dialer{}.CheckHost(host)
		if (err == nil) != want {
			t.Errorf("CheckHost(%q) = %v, want allowed %v", host, err, want)
		}
		if err != nil && !errors.Is(err, netguard.ErrForbiddenAddress) {
			t.Errorf("CheckHost(%q) = %v, want ErrForbiddenAddress", host, err)
		}
	}
	if err := (netguard.Dialer{Allow: netip.Addr.IsLoopback}).CheckHost("localhost"); err != nil {
		t.Errorf("localhost with a loopback allowance: %v", err)
	}
}
