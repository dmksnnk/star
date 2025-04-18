package host

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/platform"
	http3platform "github.com/dmksnnk/star/internal/platform/http3"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

type Client interface {
	RegisterGame(ctx context.Context, key auth.Key) (*http3.ClientConn, error)
	Forward(ctx context.Context, key auth.Key, peerID string) (http3.RequestStream, error)
}

// Registerer registers a game host.
type Registerer struct {
	Dialer platform.LocalDialer
}

func (r *Registerer) Register(ctx context.Context, client Client, key auth.Key) (*Forwarder, error) {
	conn, err := client.RegisterGame(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("register game: %w", err)
	}

	return &Forwarder{
		conn:   conn,
		client: client,
		key:    key,
		eg:     errgroup.Group{},
		dialer: r.Dialer,
	}, nil
}

// Forwarder forwards game traffic between a game host and remote server.
type Forwarder struct {
	conn   *http3.ClientConn
	client Client
	key    auth.Key

	eg     errgroup.Group
	dialer platform.LocalDialer
}

// ListenAndForward listens for incoming streams and forwards them to the game host running on the port.
// Context is used only to for dialing the game host. To cancel the forwarding, call Close().
func (f *Forwarder) ListenAndForward(ctx context.Context, port int) error {
	controlStream, err := f.conn.AcceptStream(ctx)
	if err != nil {
		return fmt.Errorf("accept stream: %w", err)
	}

	conn := http3platform.NewStreamConn(controlStream, f.conn.LocalAddr(), f.conn.RemoteAddr())
	listener := control.Listen(conn, f.client, f.key)
	defer listener.Close()

	for {
		peerStream, err := listener.AcceptForward()
		if err != nil {
			if errors.Is(err, net.ErrClosed) { // listener closed
				return nil
			}
			return fmt.Errorf("accept forward: %w", err)
		}

		hostConn, err := f.dialer.DialUDP(ctx, port)
		if err != nil {
			return fmt.Errorf("dial UDP: %w", err)
		}

		f.forward(ctx, peerStream, hostConn)
	}
}

func (f *Forwarder) forward(ctx context.Context, peerStream http3.Stream, hostConn net.Conn) {
	f.eg.Go(func() error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer hostConn.Close()

		f.eg.Go(func() error {
			select {
			case <-peerStream.Context().Done():
				cancel()
			case <-ctx.Done():
				// to not leak the goroutine
			}

			return nil
		})

		for {
			dg, err := peerStream.ReceiveDatagram(ctx)
			if err != nil {
				if errcode.IsLocalQUICConnClosed(err) { // Close() called
					return nil
				}
				if errors.Is(err, context.Canceled) {
					return nil
				}

				return fmt.Errorf("receive datagram: %w", err)
			}

			if _, err := hostConn.Write(dg); err != nil {
				peerStream.CancelRead(errcode.HostInternalError)
				return fmt.Errorf("write datagram: %w", err)
			}
		}
	})
	f.eg.Go(func() error {
		for {
			buf := make([]byte, platform.MTU)
			n, err := hostConn.Read(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					peerStream.CancelWrite(errcode.HostClosed)
					return nil
				}

				peerStream.CancelWrite(errcode.HostInternalError)
				return fmt.Errorf("read from host: %w", err)
			}

			if err := peerStream.SendDatagram(buf[:n]); err != nil {
				hostConn.Close()
				return fmt.Errorf("send datagram: %w", err)
			}
		}
	})
}

// Shutdown closes the Forwarder.
func (s *Forwarder) Close() error {
	if err := s.conn.CloseWithError(errcode.Exit, "host forwarder shutdown"); err != nil {
		return fmt.Errorf("close conn: %w", err)
	}

	return s.eg.Wait()
}
