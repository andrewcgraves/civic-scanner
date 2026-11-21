package webhooks

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"time"

	"golang.org/x/net/idna"

	"github.com/andrewcgraves/civic-scanner/api/internal/netguard"
)

const resolveTimeout = 5 * time.Second

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type AddressCheck struct {
	Resolver Resolver
	Allow    func(netip.Addr) bool
}

var (
	errNotPublicHost    = endpointError("not_public", "the URL's host must be a public internet address, not a private, loopback, link-local or reserved one")
	errNotPublicAddress = endpointError("not_public", "the URL's host resolves to an address that is not on the public internet")
	errUnresolvable     = endpointError("unresolvable_host", "the URL's host name could not be resolved to an address; check it and try again")
	errInvalidHost      = endpointError("invalid_url", "the URL's host name is not valid")
)

func (c AddressCheck) Check(ctx context.Context, endpoint string) *EndpointError {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return errInvalidHost
	}
	host := u.Hostname()
	guard := netguard.Dialer{Allow: c.Allow}
	if guard.CheckHost(host) != nil {
		return errNotPublicHost
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	name, err := lookupName(host)
	if err != nil {
		return errInvalidHost
	}
	resolver := c.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addrs, err := resolver.LookupNetIP(ctx, "ip", name)
	if err != nil || len(addrs) == 0 {
		return errUnresolvable
	}
	for _, addr := range addrs {
		if !guard.Allowed(addr) {
			return errNotPublicAddress
		}
	}
	return nil
}

func lookupName(host string) (string, error) {
	for i := range len(host) {
		if host[i] >= 0x80 {
			return idna.Lookup.ToASCII(host)
		}
	}
	return host, nil
}
