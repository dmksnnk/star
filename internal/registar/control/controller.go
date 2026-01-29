package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// Controller issues commands.
type Controller struct {
	conn *http3.ClientConn
}

// NewController creates a new controller on a connection to the agent.
func NewController(conn *quic.Conn) *Controller {
	tr := &http3.Transport{}

	return &Controller{
		conn: tr.NewClientConn(conn),
	}
}

// ConnectTo asks the peer to do hole-punching, by trying to establish
// connection to public or private addresses.
func (c *Controller) ConnectTo(ctx context.Context, public, private netip.AddrPort) error {
	cmd := ConnectCommand{
		PublicAddress:  public,
		PrivateAddress: private,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(cmd); err != nil {
		return fmt.Errorf("encode body: %w", err)
	}
	// must set fake host, as http3 requires valid URL with host
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://star/connect-to", &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.conn.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("round trip: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == StatusCodeConnectFailed {
			return ErrConnectFailed
		}

		return httpplatform.NewBadStatusCodeError(resp.StatusCode, resp.Body)
	}

	return nil
}

// ConnectCommand is a command to connect to another peer.
type ConnectCommand struct {
	PublicAddress  netip.AddrPort `json:"public_address"`
	PrivateAddress netip.AddrPort `json:"private_address"`
}

// ErrConnectFailed is returned when attempt to connect to a peer fails.
var ErrConnectFailed = errors.New("connect failed")
