package control

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
)

type Connector struct {
	tr *transport
}

// NewConnector creates a new connector on a connection.
func NewConnector(conn net.Conn) *Connector {
	return &Connector{
		tr: newTransport(conn),
	}
}

// RequestForward asks the [Listener] (follower) to initiate a forward request to the server.
func (c *Connector) RequestForward(ctx context.Context, peerID string) error {
	vals := url.Values{}
	vals.Set(peerIDQueryKey, peerID)

	u := &url.URL{
		Path:     "/",
		RawQuery: vals.Encode(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, u.String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.tr.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("round trip: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return httpplatform.NewBadStatusCodeError(resp.StatusCode, resp.Body)
	}

	return nil
}

func (c *Connector) Close() error {
	return c.tr.Close()
}
