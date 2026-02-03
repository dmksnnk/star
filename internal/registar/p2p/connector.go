package p2p

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

type badRequestError string

func (e badRequestError) Error() string {
	return string(e)
}

const (
	errcodeCancelled quic.ApplicationErrorCode = iota + 1
	errcodeBadRequest
	errcodeConflict
)

// Connector does NAT hole punching via HTTP/3 trying to directly connect two peers.
type Connector struct {
	id        string
	transport *quic.Transport
	tlsConf   *tls.Config
	quicConf  *quic.Config
	logger    *slog.Logger
}

func NewConnector(transport *quic.Transport, tlsConf *tls.Config, opts ...Option) *Connector {
	id := make([]byte, 8)
	rand.Read(id)

	c := &Connector{
		id:        hex.EncodeToString(id),
		transport: transport,
		tlsConf:   tlsConf,
		quicConf: &quic.Config{
			EnableDatagrams: true,
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

func (c *Connector) Connect(ctx context.Context, public, private netip.AddrPort) (*quic.Conn, error) {
	earlyListener, err := c.transport.ListenEarly(c.tlsConf, c.quicConf)
	if err != nil {
		return nil, fmt.Errorf("listen early: %w", err)
	}
	defer earlyListener.Close() // OK to close, as it does not close active connections

	eg, ctx := errgroup.WithContext(ctx)

	accepted := make(chan *quic.Conn)
	acceptCtx, acceptCancel := context.WithCancel(ctx)
	defer acceptCancel()
	eg.Go(func() error {
		defer close(accepted)

		return c.acceptLoop(acceptCtx, earlyListener, accepted)
	})

	publicDialled := make(chan *quic.Conn)
	publicReqCtx, publicReqCancel := context.WithCancel(ctx)
	defer publicReqCancel()
	eg.Go(func() error {
		defer close(publicDialled)

		return c.dialLoop(publicReqCtx, public, publicDialled)
	})

	var privateDialled chan *quic.Conn // created as nil, so select ignores it
	privateReqCtx, privateReqCancel := context.WithCancel(ctx)
	defer privateReqCancel()
	if public.Compare(private) != 0 {
		privateDialled = make(chan *quic.Conn) // now it is created
		eg.Go(func() error {
			defer close(privateDialled)

			return c.dialLoop(privateReqCtx, private, privateDialled)
		})
	}

	select {
	case conn := <-accepted:
		publicReqCancel()
		privateReqCancel()

		if err := eg.Wait(); err != nil {
			return nil, err
		}

		c.logger.DebugContext(ctx, "connection established via accept", "remote_addr", conn.RemoteAddr().String())
		return conn, eg.Wait()
	case conn := <-publicDialled:
		acceptCancel()
		privateReqCancel()

		if err := eg.Wait(); err != nil {
			return nil, err
		}

		c.logger.DebugContext(ctx, "connection established via public dial", "remote_addr", conn.RemoteAddr().String())
		return conn, nil
	case conn := <-privateDialled:
		acceptCancel()
		publicReqCancel()

		if err := eg.Wait(); err != nil {
			return nil, err
		}

		c.logger.DebugContext(ctx, "connection established via private dial", "remote_addr", conn.RemoteAddr().String())
		return conn, eg.Wait()
	case <-ctx.Done():
		acceptCancel()
		publicReqCancel()
		privateReqCancel()
		return nil, eg.Wait()
	}
}

func (c *Connector) acceptLoop(ctx context.Context, listener *quic.EarlyListener, accepted chan<- *quic.Conn) error {
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				c.logger.Debug("accept cancelled")
				return nil
			}
			if errors.Is(err, quic.ErrServerClosed) {
				c.logger.Debug("listener closed")
				return nil
			}

			return fmt.Errorf("accept: %w", err)
		}

		state := conn.ConnectionState()
		if !state.SupportsDatagrams.Remote {
			c.logger.Debug("client has not enabled datagrams")
			conn.CloseWithError(errcodeCancelled, "datagrams are not enabled")
			continue
		}

		ok, err := c.readHandshake(ctx, conn)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				c.logger.Debug("read handshake cancelled")
				return nil
			}

			var badReq badRequestError
			if errors.As(err, &badReq) {
				c.logger.Debug("bad request from peer", "error", err)
				conn.CloseWithError(errcodeBadRequest, err.Error())
				continue
			}

			return fmt.Errorf("handle request: %w", err)
		}

		if !ok {
			c.logger.Debug("handshake rejected, retrying")
			conn.CloseWithError(errcodeConflict, "connection rejected due to tie-breaking")
			continue
		}

		select {
		case accepted <- conn:
			c.logger.Debug("accepted connection", "remote_addr", conn.RemoteAddr().String())
			return nil
		case <-ctx.Done():
			err := fmt.Errorf("accept loop cancelled: %w", context.Cause(ctx))
			return conn.CloseWithError(errcodeCancelled, err.Error())
		}
	}
}

func (c *Connector) dialLoop(ctx context.Context, addr netip.AddrPort, dialled chan<- *quic.Conn) error {
	for {
		conn, err := c.dial(ctx, addr)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}

			return fmt.Errorf("dial: %w", err)
		}

		state := conn.ConnectionState()
		if !state.SupportsDatagrams.Remote {
			c.logger.Debug("server has not enabled datagrams")
			conn.CloseWithError(errcodeCancelled, "datagrams are not enabled")
			continue
		}

		ok, err := c.sendHandshake(ctx, conn)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return conn.CloseWithError(errcodeCancelled, fmt.Sprintf("cancelled: %v", context.Cause(ctx)))
			}
			if isCancelledStreamError(err) {
				return conn.CloseWithError(errcodeCancelled, fmt.Sprintf("cancelled: %v", context.Cause(ctx)))
			}

			return fmt.Errorf("send handshake to %s: %w", addr, err)
		}

		if !ok {
			c.logger.DebugContext(ctx, "handshake rejected by peer, retrying")
			conn.CloseWithError(errcodeCancelled, "closing connection after rejection")
			continue
		}

		select {
		case dialled <- conn:
			c.logger.DebugContext(ctx, "handshake successful", "remote_addr", conn.RemoteAddr().String())
			return nil
		case <-ctx.Done():
			err := fmt.Errorf("dial connection cancelled: %w", context.Cause(ctx))
			return conn.CloseWithError(errcodeCancelled, err.Error())
		}
	}
}

func (c *Connector) dial(ctx context.Context, addr netip.AddrPort) (*quic.Conn, error) {
	conn, err := c.transport.Dial(ctx, net.UDPAddrFromAddrPort(addr), c.tlsConf, c.quicConf)
	if err != nil {
		return nil, fmt.Errorf("dial QUIC: %w", err)
	}

	return conn, nil
}

// readHandshake returns true if handshake was successful,
// false if handshake was rejected. Connection is not closed by this function.
func (c *Connector) readHandshake(ctx context.Context, conn *quic.Conn) (bool, error) {
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return false, fmt.Errorf("accept stream: %w", err)
	}
	defer func() {
		stream.CancelRead(quic.StreamErrorCode(quic.NoError))
		stream.Close()
	}()

	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		return false, fmt.Errorf("read request: %w", err)
	}

	peerID := req.Header.Get("X-Connector-ID")
	if peerID == "" {
		return false, badRequestError("missing X-Connector-ID")
	}

	if peerID > c.id {
		c.logger.Debug("tie-breaking: peer has larger ID, rejecting connection", "our_id", c.id, "peer_id", peerID)
		return false, nil
	}
	c.logger.Debug("tie-breaking: accepting connection", "peer_id", peerID)

	httpWrite(stream, http.NoBody, http.StatusOK)

	return true, nil
}

// sendHandshake returns true if handshake was successful,
// false if handshake was rejected. Connection is not closed by this function.
func (c *Connector) sendHandshake(ctx context.Context, conn *quic.Conn) (bool, error) {
	req, err := c.newRequest(ctx)
	if err != nil {
		return false, err
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return false, fmt.Errorf("open stream: %w", err)
	}
	defer func() {
		stream.CancelRead(quic.StreamErrorCode(quic.NoError))
		stream.Close()
	}()

	// cancel stream read when context is cancelled to unblock http.ReadResponse
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			stream.CancelRead(quic.StreamErrorCode(errcodeCancelled))
		case <-done:
		}
	}()

	if err := req.Write(stream); err != nil {
		return false, fmt.Errorf("write request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		if isConflictError(err) {
			return false, nil
		}

		return false, fmt.Errorf("read response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, httpplatform.NewBadStatusCodeError(resp.StatusCode, resp.Body)
	}

	return true, nil
}

func isConflictError(err error) bool {
	var appErr *quic.ApplicationError
	if errors.As(err, &appErr) {
		return appErr.Remote && appErr.ErrorCode == errcodeConflict
	}

	return false
}

func isCancelledStreamError(err error) bool {
	var streamErr *quic.StreamError
	return errors.As(err, &streamErr) &&
		streamErr.ErrorCode == quic.StreamErrorCode(errcodeCancelled) &&
		!streamErr.Remote
}

func (c *Connector) newRequest(ctx context.Context) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "/connect", http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Connector-ID", c.id)
	return req, nil
}

func httpWrite(w io.Writer, body io.ReadCloser, status int) {
	r := http.Response{
		StatusCode: status,
		Body:       body,
	}

	r.Write(w)
}

// Option is a functional option for configuring a Connector.
type Option func(*Connector)

// WithQuicConfig sets the QUIC config for the Connector.
func WithQuicConfig(quicConf *quic.Config) Option {
	return func(c *Connector) {
		c.quicConf = quicConf
	}
}

// WithLogger sets the logger for the Connector.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Connector) {
		c.logger = logger
	}
}
