package fetch

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/netguard"
)

const dialTimeout = 30 * time.Second

type Option func(*options)

type options struct {
	logger *slog.Logger
	allow  func(netip.Addr) bool
}

func WithLogger(logger *slog.Logger) Option {
	return func(o *options) { o.logger = logger }
}

func AllowAddress(allow func(netip.Addr) bool) Option {
	return func(o *options) { o.allow = allow }
}

func buildOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return o
}

func (o options) guard() netguard.Dialer {
	return netguard.Dialer{Allow: o.allow, Timeout: dialTimeout}
}

func (o options) refused(ctx context.Context, fetcher, target string, err error) *Error {
	var refused *netguard.RefusedError
	if !errors.As(err, &refused) {
		return nil
	}
	o.logger.WarnContext(ctx, "fetch refused a non-public address",
		"fetcher", fetcher, "host", refused.Host, "address", refused.Address, "url", target)
	return &Error{Err: refused}
}
