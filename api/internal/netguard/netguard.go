package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

const defaultDialTimeout = 10 * time.Second

var (
	ErrForbiddenAddress = errors.New("address is not a public internet address")
	nonPublicPrefixes   = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("fec0::/10"),
	}
	localhost = netip.AddrFrom4([4]byte{127, 0, 0, 1})
)

func PublicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

type RefusedError struct {
	Host    string
	Address string
}

func (e *RefusedError) Error() string {
	if e.Address == "" || e.Address == e.Host {
		return fmt.Sprintf("refused to connect to %s: %v", e.Host, ErrForbiddenAddress)
	}
	return fmt.Sprintf("refused to connect to %s at %s: %v", e.Host, e.Address, ErrForbiddenAddress)
}

func (e *RefusedError) Unwrap() error { return ErrForbiddenAddress }

type Dialer struct {
	Allow   func(netip.Addr) bool
	Timeout time.Duration
}

func (d Dialer) Allowed(addr netip.Addr) bool {
	if d.Allow != nil {
		return d.Allow(addr)
	}
	return PublicAddress(addr)
}

func (d Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	if addr, err := netip.ParseAddr(host); err == nil && !d.Allowed(addr) {
		return nil, &net.OpError{Op: "dial", Net: network, Err: &RefusedError{Host: host, Address: address}}
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}
	dialer := &net.Dialer{
		Timeout: timeout,
		Control: func(_, resolved string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(resolved)
			if err != nil || !d.Allowed(ap.Addr()) {
				return &RefusedError{Host: host, Address: resolved}
			}
			return nil
		},
	}
	return dialer.DialContext(ctx, network, address)
}

func (d Dialer) CheckHost(host string) error {
	name := strings.TrimSuffix(strings.ToLower(host), ".")
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		if d.Allowed(localhost) {
			return nil
		}
		return &RefusedError{Host: host}
	}
	addr, err := netip.ParseAddr(name)
	if err != nil {
		if numericHost(name) {
			return &RefusedError{Host: host}
		}
		return nil
	}
	if !d.Allowed(addr) {
		return &RefusedError{Host: host}
	}
	return nil
}

func numericHost(name string) bool {
	labels := strings.Split(name, ".")
	last := labels[len(labels)-1]
	if last == "" {
		return false
	}
	if strings.HasPrefix(last, "0x") {
		return true
	}
	return strings.Trim(last, "0123456789") == ""
}
