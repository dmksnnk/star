package relay

import (
	"log/slog"
	"net/netip"
	"time"
)

type Option func(*UDPRelay)

// WithWorkers sets the number of worker goroutines for the UDP relay.
func WithWorkers(workers int) Option {
	return func(r *UDPRelay) {
		r.workers = workers
	}
}

// WithLogger sets the logger for the UDP relay.
func WithLogger(logger *slog.Logger) Option {
	return func(r *UDPRelay) {
		r.logger = logger
	}
}

// WithRouteTTL sets the TTL for unused routes.
// Default is 30 minutes.
func WithRouteTTL(ttl time.Duration) Option {
	return func(r *UDPRelay) {
		r.routes.Close() // stop previous TTLMap goroutine
		r.routes = NewTTLMap[netip.AddrPort, netip.AddrPort](ttl)
	}
}
