package peer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/platform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

type Client interface {
	ConnectGame(context.Context, auth.Key, string) (http3.RequestStream, error)
}

type Connector struct {
	Config platform.LocalListener
	Port   int
	Logger *slog.Logger
}

func (c *Connector) ConnectAndListen(ctx context.Context, client Client, key auth.Key, peerID string) (*Forwarder, error) {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}

	conn, err := client.ConnectGame(ctx, key, peerID)
	if err != nil {
		return nil, fmt.Errorf("connect game: %w", err)
	}
	c.Logger.Debug("connected to the game", "key", key, "peer_id", peerID)

	gameConn, err := c.Config.ListenUDP(ctx, c.Port)
	if err != nil {
		return nil, fmt.Errorf("listen packet: %w", err)
	}
	c.Logger.Debug("listening for game connections", "addr", gameConn.LocalAddr())

	return &Forwarder{
		gameConn:   gameConn,
		hostStream: conn,
		logger:     c.Logger,
	}, nil
}

type Forwarder struct {
	gameConn   net.PacketConn
	hostStream http3.RequestStream
	logger     *slog.Logger
	closed     atomic.Bool
}

// LocalAddr returns the local network address of listener for the game connections.
func (f *Forwarder) LocalAddr() net.Addr {
	return f.gameConn.LocalAddr()
}

// AcceptAndForward accepts a game connection and forwards it to the host.
// Call Close to stop forwarding.
func (f *Forwarder) AcceptAndForward() error {
	gameConn, initData, err := accept(f.gameConn, f.logger)
	if err != nil {
		if f.closed.Load() && errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("accept game conn: %w", err)
	}
	defer gameConn.Close()

	f.logger.Debug("accepted game connection", "addr", gameConn.RemoteAddr())
	if err := f.hostStream.SendDatagram(initData); err != nil {
		gameConn.Close()
		return fmt.Errorf("write init data: %w", err)
	}
	f.logger.Debug("forwarded init data to host")

	return f.forward(gameConn)
}

func (f *Forwarder) forward(gameConn net.Conn) error {
	var eg errgroup.Group
	eg.Go(func() error {
		defer gameConn.Close()

		for {
			dg, err := f.hostStream.ReceiveDatagram(f.hostStream.Context())
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				// closed in another goroutine
				if errcode.IsLocalStreamError(err, errcode.PeerClosed) {
					return nil
				}
				// closed by the host
				if errcode.IsRemoteStreamError(err, errcode.HostClosed) {
					return nil
				}
				// http3 connection closed (closed client to the registrar API)
				if errcode.IsLocalQUICConnClosed(err) {
					return nil
				}
				return fmt.Errorf("receive datagram: %w", err)
			}

			if _, err := gameConn.Write(dg); err != nil {
				f.hostStream.CancelRead(errcode.PeerInternalError)
				return fmt.Errorf("write datagram: %w", err)
			}
		}
	})

	eg.Go(func() error {
		for {
			buf := make([]byte, platform.MTU)
			n, err := gameConn.Read(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					f.hostStream.CancelWrite(errcode.PeerClosed)
					return nil
				}

				f.hostStream.CancelWrite(errcode.PeerInternalError)
				return fmt.Errorf("read from game: %w", err)
			}

			if err := f.hostStream.SendDatagram(buf[:n]); err != nil {
				gameConn.Close()
				return fmt.Errorf("send datagram: %w", err)
			}
		}
	})

	return eg.Wait()
}

func (f *Forwarder) Close() error {
	if f.closed.Load() {
		return nil
	}
	f.closed.Store(true)

	f.hostStream.CancelRead(errcode.PeerClosed)

	if err := f.gameConn.Close(); err != nil {
		return fmt.Errorf("close game conn: %w", err)
	}

	return nil
}

func accept(gameConn net.PacketConn, logger *slog.Logger) (net.Conn, []byte, error) {
	buf := make([]byte, platform.MTU)
	n, addr, err := gameConn.ReadFrom(buf)

	return NewUDPConn(gameConn, addr, logger), buf[:n], err
}

// UDPConn is a net.Conn that only communicates with a specific remote address.
// It assumes only one address will be used (however, UDP is connectionless).
// If client reconnects with a different address, it will update the address.
type UDPConn struct {
	net.PacketConn
	mux    sync.RWMutex
	addr   net.Addr
	logger *slog.Logger
}

func NewUDPConn(conn net.PacketConn, addr net.Addr, logger *slog.Logger) *UDPConn {
	return &UDPConn{
		PacketConn: conn,
		addr:       addr,
		logger:     logger,
	}
}

func (w *UDPConn) RemoteAddr() net.Addr {
	w.mux.RLock()
	defer w.mux.RUnlock()

	return w.addr
}

func (w *UDPConn) Read(p []byte) (int, error) {
	n, addr, err := w.ReadFrom(p)
	if err != nil {
		return n, err
	}

	currentAddr := w.RemoteAddr()
	if addr.String() != currentAddr.String() {
		w.logger.Debug("client changed address",
			"old", currentAddr.String(),
			"new", addr.String())

		w.mux.Lock()
		w.addr = addr
		w.mux.Unlock()
	}

	return n, err
}

func (w *UDPConn) Write(p []byte) (int, error) {
	w.mux.RLock()
	defer w.mux.RUnlock()

	return w.WriteTo(p, w.addr)
}
