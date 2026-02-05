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
	"github.com/quic-go/quic-go/http3"
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

	c.logger.DebugContext(ctx, "starting connection attempts",
		"listen_addr", earlyListener.Addr().String(),
		"public_addr", public.String(),
		"private_addr", private.String(),
	)

	eg, ctx := errgroup.WithContext(ctx)

	connected := make(chan *quic.Conn)
	attemptsCtx, cancelAttempts := context.WithCancel(ctx)
	defer cancelAttempts()

	eg.Go(func() error {
		return c.acceptLoop(attemptsCtx, earlyListener, connected)
	})

	eg.Go(func() error {
		return c.dialLoop(attemptsCtx, public, connected)
	})

	if public.Compare(private) != 0 {
		eg.Go(func() error {
			return c.dialLoop(attemptsCtx, private, connected)
		})
	}

	select {
	case conn := <-connected:
		cancelAttempts()
		if err := eg.Wait(); err != nil {
			return nil, err
		}

		c.logger.DebugContext(ctx, "connection established",
			"via", connectedVia(conn, public, private),
			"remote_addr", conn.RemoteAddr().String(),
		)
		return conn, nil
	case <-ctx.Done():
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
				conn.CloseWithError(errcodeCancelled, err.Error())
				continue
			}

			if isRequestCancelledError(err, true) {
				c.logger.Debug("request cancelled remotely", "err", err)
				conn.CloseWithError(errcodeCancelled, err.Error())
				continue
			}

			if isRemoteCancelledError(err) {
				c.logger.Debug("peer cancelled connection", "err", err)
				continue // connection already closed by peer
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
		conn, err := c.transport.Dial(ctx, net.UDPAddrFromAddrPort(addr), c.tlsConf, c.quicConf)
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
				conn.CloseWithError(errcodeCancelled, err.Error())
				continue
			}

			if isRemoteConflictError(err) {
				c.logger.DebugContext(ctx, "handshake rejected by peer, retrying")
				// no need to close conn, as other side has already closed it
				continue
			}

			if isRemoteCancelledError(err) {
				c.logger.DebugContext(ctx, "peer cancelled connection", "err", err)
				continue // connection already closed by peer
			}

			return fmt.Errorf("send handshake to %s: %w", addr, err)
		}

		if !ok {
			c.logger.DebugContext(ctx, "handshake rejected by peer, retrying")
			conn.CloseWithError(errcodeConflict, "closing connection after rejection")
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

// readHandshake returns true if handshake was successful,
// false if handshake was rejected. Connection is not closed by this function.
func (c *Connector) readHandshake(ctx context.Context, conn *quic.Conn) (bool, error) {
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return false, fmt.Errorf("accept stream: %w", err)
	}

	stop := context.AfterFunc(ctx, func() {
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
	})
	defer stop()

	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		if isRequestRejectedError(err, false) { // context cancelled and we rejected it
			return false, context.Canceled
		}
		return false, fmt.Errorf("read request: %w", err)
	}

	peerID := req.Header.Get("X-Connector-ID")
	if peerID == "" {
		return false, badRequestError("missing X-Connector-ID")
	}

	if peerID > c.id {
		c.logger.Debug("tie-breaking: peer has larger ID, rejecting connection", "our_id", c.id, "peer_id", peerID)
		if err := httpWrite(stream, http.NoBody, http.StatusConflict); err != nil {
			if isRequestRejectedError(err, false) { // context cancelled and we rejected it
				return false, context.Canceled
			}
			return false, fmt.Errorf("write response: %w", err)
		}
		// let other side know we are finished
		return false, stream.Close()
	}
	c.logger.Debug("tie-breaking: accepting connection", "peer_id", peerID)

	if err := httpWrite(stream, http.NoBody, http.StatusOK); err != nil {
		if isRequestRejectedError(err, false) { // context cancelled and we rejected it
			return false, context.Canceled
		}
		return false, fmt.Errorf("write response: %w", err)
	}

	return true, stream.Close()
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

	// cancel stream when context is cancelled to unblock http.ReadResponse
	stop := context.AfterFunc(ctx, func() {
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	})
	defer stop()

	if err := req.Write(stream); err != nil {
		if isRequestCancelledError(err, false) { // context cancelled, so we cancelled request
			return false, context.Canceled
		}
		return false, fmt.Errorf("write request: %w", err)
	}

	if err := stream.Close(); err != nil { // let other side know we have finished sending request
		return false, fmt.Errorf("close stream: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		if isRequestCancelledError(err, false) { // context cancelled, so we cancelled request
			return false, context.Canceled
		}
		return false, fmt.Errorf("read response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return false, nil
	}

	if resp.StatusCode != http.StatusOK {
		return false, httpplatform.NewBadStatusCodeError(resp.StatusCode, resp.Body)
	}

	return true, nil
}

func isRequestCancelledError(err error, isRemote bool) bool {
	var streamErr *quic.StreamError
	return errors.As(err, &streamErr) &&
		streamErr.ErrorCode == quic.StreamErrorCode(http3.ErrCodeRequestCanceled) &&
		streamErr.Remote == isRemote
}

func isRequestRejectedError(err error, isRemote bool) bool {
	var streamErr *quic.StreamError
	return errors.As(err, &streamErr) &&
		streamErr.ErrorCode == quic.StreamErrorCode(http3.ErrCodeRequestRejected) &&
		streamErr.Remote == isRemote
}

func isRemoteConflictError(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) &&
		appErr.ErrorCode == errcodeConflict &&
		appErr.Remote
}

func isRemoteCancelledError(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) &&
		appErr.ErrorCode == errcodeCancelled &&
		appErr.Remote
}

func (c *Connector) newRequest(ctx context.Context) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "/connect", http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Connector-ID", c.id)
	return req, nil
}

func httpWrite(w io.Writer, body io.ReadCloser, status int) error {
	r := http.Response{
		StatusCode: status,
		Body:       body,
	}

	return r.Write(w)
}

func connectedVia(conn *quic.Conn, public, private netip.AddrPort) string {
	remote := conn.RemoteAddr().(*net.UDPAddr).AddrPort()
	switch remote {
	case public:
		return "public dial"
	case private:
		return "private dial"
	default:
		return "accept"
	}
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
