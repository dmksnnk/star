package relay

import (
	"log/slog"
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
