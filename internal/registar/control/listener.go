package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go/http3"
)

const peerIDQueryKey = "peer_id"

type Registar interface {
	// Forwards ask server to forward a peer connection.
	Forward(ctx context.Context, key auth.Key, peerID string) (http3.RequestStream, error)
}

type Listener struct {
	tr       *transport
	registar Registar
	key      auth.Key
	closed   atomic.Bool
}

// Listen creates a new listener on a connection.
func Listen(conn net.Conn, registar Registar, key auth.Key) *Listener {
	return &Listener{
		tr:       newTransport(conn),
		registar: registar,
		key:      key,
	}
}

// AcceptForward accepts a CONNECT request from [Connector] and then asks the server to forward the connection.
// Returns net.ErrClosed if the listener is closed.
func (c *Listener) AcceptForward() (http3.RequestStream, error) {
	for {
		if c.closed.Load() {
			return nil, net.ErrClosed
		}

		var stream http3.RequestStream
		err := c.tr.Handle(func(r *http.Request) *http.Response {
			if r.Method != http.MethodConnect {
				return errorResponse("unexpected method", http.StatusBadRequest)
			}

			peerID := r.URL.Query().Get(peerIDQueryKey)
			if peerID == "" {
				return errorResponse("missing peer id", http.StatusBadRequest)
			}

			conn, err := c.registar.Forward(context.Background(), c.key, peerID)
			if err != nil {
				return errorResponse(fmt.Sprintf("forward: %s", err), http.StatusInternalServerError)
			}
			stream = conn

			return &http.Response{StatusCode: http.StatusOK}
		})
		if err != nil {
			if errors.Is(err, io.ErrClosedPipe) {
				return nil, net.ErrClosed
			}
			if errcode.IsLocalQUICConnClosed(err) {
				return nil, net.ErrClosed
			}

			return nil, fmt.Errorf("handle: %w", err)
		}

		if stream != nil { // if obtained a peer connection, return it
			return stream, nil
		}
	}
}

func errorResponse(err string, code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader([]byte(err))),
	}
}

func (c *Listener) Close() error {
	c.closed.Store(true)
	return c.tr.Close()
}
