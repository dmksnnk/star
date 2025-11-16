package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
)

// Controller issues commands.
type Controller struct {
	tr *transport
}

// NewController creates a new controller on a connection to the agent.
func NewController(conn io.ReadWriter) *Controller {
	return &Controller{
		tr: newTransport(conn),
	}
}

// ConnectTo asks the peer to do hole-punching, by trying to establish
// connection to public or private addresses.
func (c *Controller) ConnectTo(ctx context.Context, public, private netip.AddrPort) error {
	cmd := ConnectCommand{
		PublicAddress:  public,
		PrivateAddress: private,
	}

	if err := c.do(ctx, http.MethodPost, "/connect-to", cmd); err != nil {
		var e *httpplatform.BadStatusCodeError
		if errors.As(err, &e) && e.StatusCode == StatusCodeConnectFailed {
			return ErrConnectFailed
		}

		return err
	}

	return nil
}

func (c *Controller) do(ctx context.Context, method, url string, body any) error {
	var reqBody io.Reader = http.NoBody
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("encode body: %w", err)
		}

		reqBody = &buf
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
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

// ConnectCommand is a command to connect to another peer.
type ConnectCommand struct {
	PublicAddress  netip.AddrPort `json:"public_address"`
	PrivateAddress netip.AddrPort `json:"private_address"`
}

// ErrConnectFailed is returned when attempt to connect to a peer fails.
var ErrConnectFailed = errors.New("connect failed")
