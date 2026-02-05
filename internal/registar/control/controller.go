package control

import (
	"bufio"
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

// ErrConnectFailed is returned when attempt to connect to a peer fails.
var ErrConnectFailed = errors.New("connect failed")

// Controller issues commands.
type Controller struct {
	conn *quic.Conn
}

// NewController creates a new controller on a connection to the agent.
func NewController(conn *quic.Conn) *Controller {
	return &Controller{
		conn: conn,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://star/connect-to", &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	stream, err := c.conn.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	// cancel stream if context is canceled
	stop := context.AfterFunc(ctx, func() {
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	})
	defer stop()

	if err := req.Write(stream); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	// let the other side know that request is complete
	if err := stream.Close(); err != nil {
		return fmt.Errorf("close stream after request write: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
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
