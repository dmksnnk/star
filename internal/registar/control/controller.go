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

// InitRelayStream asks the [Agent] (follower) to initiate a forward request to the relay.
func (c *Controller) InitRelayStream(ctx context.Context, peerID string) error {
	cmd := RequestForwardCommand{
		PeerID: peerID,
	}
	return c.do(ctx, http.MethodPost, "/relay-peer", cmd)
}

// JoinViaRelay asks the [Agent] (follower) to join a session via relay.
func (c *Controller) JoinViaRelay(ctx context.Context, peerID string) error {
	cmd := RequestJoin{
		PeerID: peerID,
	}
	return c.do(ctx, http.MethodPost, "/join-relay", cmd)
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

// RequestForwardCommand is a command to ask Registar to forward peer connection to you.
type RequestForwardCommand struct {
	PeerID string `json:"peer_id"`
}

type RequestJoin struct {
	PeerID string `json:"peer_id"`
}

var ErrConnectFailed = errors.New("connect failed")
