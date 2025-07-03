package udp

import (
	"context"
	"time"
)

// newDeadline creates a new context with a deadline.
// If the provided time is zero, it returns a context that never cancels.
// Used for creating deadlines for UDP connections.
func newDeadline(t time.Time) (context.Context, context.CancelFunc) {
	if t.IsZero() {
		// If no deadline, create a context that never cancels
		ctx, cancel := context.WithCancel(context.Background())
		return ctx, cancel
	}

	return context.WithDeadline(context.Background(), t)
}
