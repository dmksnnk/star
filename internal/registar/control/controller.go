package control

import (
	"bufio"
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

// ErrConnectFailed is returned when attempt to connect to a peer fails.
var ErrConnectFailed = errors.New("connect failed")

type Stream interface {
	io.Reader
	io.Writer
	Context() context.Context
}

// ConnectTo sends a command to connect to another peer and waits for response.
// Returns ErrConnectFailed on StatusCodeConnectFailed.
func ConnectTo(str Stream, public, private netip.AddrPort) error {
	cmd := ConnectCommand{
		PublicAddress:  public,
		PrivateAddress: private,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(cmd); err != nil {
		return fmt.Errorf("encode body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, "https://star/connect-to", &buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	if err := req.Write(str); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(str), req)
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

func ConnectViaRelay(str Stream, sessionID string) error {
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://star/relay/%s", sessionID), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	if err := req.Write(str); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(str), req)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
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
