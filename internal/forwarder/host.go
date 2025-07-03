package forwarder

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
	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

// PeerConnectionController allows to register a host and forward connections to it.
type PeerConnectionController interface {
	control.ConnectionForwarder
	RegisterHost(ctx context.Context, key auth.Key) (*http3.RequestStream, error)
}

// RegisterHost registers a host on the server.
func RegisterHost(ctx context.Context, client PeerConnectionController, key auth.Key) (*control.Listener, error) {
	stream, err := client.RegisterHost(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("register host: %w", err)
	}

	conn := http3platform.NewStreamConn(stream)

	return control.Listen(conn, client, key), nil
}

// Host forwards game traffic between a game host and a peer.
type Host struct {
	dialer *platform.LocalDialer
	logger *slog.Logger

	errHandlers []func(error)

	mux        sync.Mutex
	links      map[*link]struct{}
	linksGroup sync.WaitGroup
	listeners  map[*control.Listener]struct{}
}

func NewHost(ops ...HostOption) *Host {
	h := &Host{
		dialer:    &platform.LocalDialer{},
		logger:    slog.Default(),
		links:     make(map[*link]struct{}),
		listeners: make(map[*control.Listener]struct{}),
	}
	for _, op := range ops {
		op(h)
	}
	return h
}

// Forward accepts incoming peer streams and forwards them to the game host running on the port.
// Context is used only to for dialing the game host. To cancel the forwarding, call [Host.Close]
func (h *Host) Forward(ctx context.Context, listener *control.Listener, port int) error {
	defer listener.Close()
	h.trackListener(listener, true)
	defer h.trackListener(listener, false)

	for {
		peerStream, err := listener.AcceptForward()
		if err != nil {
			if errors.Is(err, net.ErrClosed) { // listener closed
				return nil
			}
			return fmt.Errorf("accept forward: %w", err)
		}
		h.logger.Debug("accepted forward request")

		hostConn, err := h.dialer.DialUDP(ctx, port)
		if err != nil {
			return fmt.Errorf("dial UDP: %w", err)
		}
		h.logger.Debug("dialed host", "local_addr", hostConn.LocalAddr().String(), "remote_addr", hostConn.RemoteAddr().String())

		l := newLink(hostConn, peerStream)
		h.trackLink(l, true)
		go func() {
			defer h.trackLink(l, false)
			err := l.serve(ctx)
			for _, eh := range h.errHandlers {
				eh(err)
			}
		}()
	}
}

func (h *Host) trackListener(l *control.Listener, add bool) {
	h.mux.Lock()
	defer h.mux.Unlock()

	if add {
		h.listeners[l] = struct{}{}
	} else {
		delete(h.listeners, l)
	}
}

func (h *Host) trackLink(l *link, add bool) {
	h.mux.Lock()
	defer h.mux.Unlock()

	if add {
		h.links[l] = struct{}{}
		h.linksGroup.Add(1)
	} else {
		delete(h.links, l)
		h.linksGroup.Done()
	}
}

// Close the [Host].
// It closes all listeners and links, and waits for all links to finish.
func (h *Host) Close() error {
	h.mux.Lock()
	// Close all listeners first to avoid new connections
	var err error
	for l := range h.listeners {
		if closeErr := l.Close(); closeErr != nil {
			err = closeErr
		}
	}

	for l := range h.links {
		if closeErr := l.close(); closeErr != nil {
			err = closeErr
		}
	}
	h.mux.Unlock()

	h.linksGroup.Wait()
	return err
}

// link is connection between [net.Conn] and HTTP3 stream
type link struct {
	conn   *onceCloseConn
	stream *http3.RequestStream
	closed atomic.Bool
}

func newLink(conn net.Conn, stream *http3.RequestStream) *link {
	return &link{
		conn:   &onceCloseConn{Conn: conn},
		stream: stream,
	}
}

func (c *link) serve(ctx context.Context) error {
	var eg errgroup.Group
	eg.Go(func() error {
		defer c.close()
		// Close closes the send-direction of the stream.
		// It does not close the receive-direction of the stream.
		defer c.stream.Close()

		buf := make([]byte, platform.MTU)
		for {
			n, err := c.conn.Read(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) && c.closed.Load() {
					c.stream.CancelRead(errcode.Cancelled)
					return nil // closing
				}
				c.stream.CancelRead(errcode.Unknown)
				return fmt.Errorf("read from connection: %w", err)
			}

			if err := c.stream.SendDatagram(buf[:n]); err != nil {
				c.conn.Close()
				// cancelled in another thread
				if errcode.IsLocalStreamError(err, errcode.Cancelled) {
					return nil
				}
				return fmt.Errorf("send datagram: %w", err)
			}
		}
	})

	eg.Go(func() error {
		for {
			dg, err := c.stream.ReceiveDatagram(ctx)
			if err != nil {
				c.conn.Close()
				// cancelled in another thread
				if errcode.IsLocalStreamError(err, errcode.Cancelled) {
					return nil
				}

				return fmt.Errorf("receive datagram: %w", err)
			}

			if _, err := c.conn.Write(dg); err != nil {
				if errors.Is(err, net.ErrClosed) && c.closed.Load() {
					c.stream.CancelRead(errcode.Cancelled)
					return nil // closing
				}
				c.stream.CancelRead(errcode.Unknown)
				return fmt.Errorf("write datagram: %w", err)
			}
		}
	})

	return eg.Wait()
}

func (c *link) close() error {
	c.closed.Store(true)
	return c.conn.Close()
}

// onceCloseConn protects net.Conn from closing it multiple times.
type onceCloseConn struct {
	net.Conn
	once     sync.Once
	closeErr error
}

func (oc *onceCloseConn) Close() error {
	oc.once.Do(oc.close)
	return oc.closeErr
}

func (oc *onceCloseConn) close() {
	oc.closeErr = oc.Conn.Close()
}

type HostOption func(*Host)

func WithDialer(d *platform.LocalDialer) HostOption {
	return func(h *Host) {
		h.dialer = d
	}
}

func WithHostLogger(l *slog.Logger) HostOption {
	return func(h *Host) {
		h.logger = l
	}
}

func WithNotifyDisconnect(eh func(error)) HostOption {
	return func(h *Host) {
		h.errHandlers = append(h.errHandlers, eh)
	}
}
