package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
)

// Controller issues commands.
type Controller struct {
	tr *transport
}

// NewRegistar creates a new connector on a connection.
func NewRegistar(conn io.ReadWriter) *Controller {
	return &Controller{
		tr: newTransport(conn),
	}
}

// DoRequestForward asks the [Agent] (follower) to initiate a forward request to the server.
func (c *Controller) DoRequestForward(ctx context.Context, peerID string) error {
	cmd := RequestForwardCommand{
		PeerID: peerID,
	}
	return c.do(ctx, http.MethodPost, "/forward", cmd)
}

// ConnectTo asks the peer to do hole-punching, by trying to establish
func (c *Controller) ConnectTo(ctx context.Context, public, private string) error {
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
	var reqBody io.ReadCloser = http.NoBody
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("encode body: %w", err)
		}

		reqBody = io.NopCloser(&buf)
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
	PublicAddress  string `json:"public_address"`
	PrivateAddress string `json:"private_address"`
}

// RequestForwardCommand is a command to ask Registar to forward peer connection to you.
type RequestForwardCommand struct {
	PeerID string `json:"peer_id"`
}

var ErrConnectFailed = errors.New("connect failed")
