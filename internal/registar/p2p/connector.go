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

var errConflict = errors.New("connection conflict")

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
	defer earlyListener.Close()

	eg, ctx := errgroup.WithContext(ctx)

	accepted := make(chan *quic.Conn)
	acceptCtx, acceptCancel := context.WithCancel(ctx)
	defer acceptCancel()
	eg.Go(func() error {
		for {
			conn, err := earlyListener.Accept(acceptCtx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					c.logger.Debug("accept cancelled")
					return nil
				}
				if errors.Is(err, quic.ErrServerClosed) {
					return nil
				}

				return fmt.Errorf("accept: %w", err)
			}

			state := conn.ConnectionState()
			if !state.SupportsDatagrams {
				c.logger.Debug("client has not enabled datagrams")
				conn.CloseWithError(errcodeCancelled, "datagrams are not enabled")
				continue
			}

			ok, err := c.acceptConnection(acceptCtx, conn)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}

				return fmt.Errorf("handle request: %w", err)
			}

			if !ok {
				c.logger.Debug("unsuccessful accept connection attempt, retrying")
				continue
			}

			select {
			case accepted <- conn:
				c.logger.Debug("accepted connection",
					"local_addr", conn.LocalAddr().String(), "remote_addr", conn.RemoteAddr().String())
				return nil
			case <-acceptCtx.Done():
				err := fmt.Errorf("server connection cancelled: %w", context.Cause(acceptCtx))
				return conn.CloseWithError(errcodeCancelled, err.Error())
			}
		}
	})

	publicDialled := make(chan *quic.Conn)
	publicReqCtx, publicReqCancel := context.WithCancel(ctx)
	defer publicReqCancel()
	eg.Go(func() error {
		for {
			conn, err := c.dial(publicReqCtx, public)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}

				return fmt.Errorf("dial public address: %w", err)
			}

			state := conn.ConnectionState()
			if !state.SupportsDatagrams {
				c.logger.Debug("server has not enabled datagrams")
				conn.CloseWithError(errcodeCancelled, "datagrams are not enabled")
				continue
			}

			if err := c.sendRequest(publicReqCtx, conn); err != nil {
				if errors.Is(err, errConflict) {
					c.logger.Debug("dial public address rejected by peer, retrying")
					conn.CloseWithError(errcodeCancelled, "closing connection because of rejection")
					continue
				}
				if errors.Is(err, context.Canceled) {
					err := fmt.Errorf("public address connection cancelled: %w", context.Cause(publicReqCtx))
					return conn.CloseWithError(errcodeCancelled, err.Error())
				}

				return fmt.Errorf("send request to public address: %w", err)
			}

			select {
			case publicDialled <- conn:
				c.logger.Debug("established connection to public adddress",
					"local_addr", conn.LocalAddr().String(), "remote_addr", conn.RemoteAddr().String())
				return nil
			case <-publicReqCtx.Done():
				err := fmt.Errorf("public connection cancelled: %w", context.Cause(publicReqCtx))
				return conn.CloseWithError(errcodeCancelled, err.Error())
			}
		}
	})

	var privateDialled chan *quic.Conn // created as nil, so select ignores it
	privateReqCtx, privateReqCancel := context.WithCancel(ctx)
	defer privateReqCancel()
	if public != private { // TODO: refactor
		privateDialled = make(chan *quic.Conn)
		eg.Go(func() error {
			for {
				conn, err := c.dial(privateReqCtx, private)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return nil
					}

					return fmt.Errorf("dial private address: %w", err)
				}

				state := conn.ConnectionState()
				if !state.SupportsDatagrams {
					c.logger.Debug("server has not enabled datagrams")
					conn.CloseWithError(errcodeCancelled, "datagrams are not enabled")
					continue
				}

				if err := c.sendRequest(privateReqCtx, conn); err != nil {
					if errors.Is(err, errConflict) {
						c.logger.Debug("dial private address rejected by peer, retrying")
						conn.CloseWithError(errcodeCancelled, "closing after rejection")
						continue
					}
					if errors.Is(err, context.Canceled) {
						err := fmt.Errorf("private connection cancelled: %w", context.Cause(privateReqCtx))
						return conn.CloseWithError(errcodeCancelled, err.Error())
					}

					return fmt.Errorf("send request to private address: %w", err)
				}

				select {
				case privateDialled <- conn:
					c.logger.Debug("established connetion to private address", "remote_addr", conn.RemoteAddr().String())
					return nil
				case <-privateReqCtx.Done():
					message := fmt.Sprintf("private connection cancelled: %s", context.Cause(privateReqCtx))
					return conn.CloseWithError(errcodeCancelled, message)
				}
			}
		})
	}

	select {
	case conn := <-accepted:
		publicReqCancel()
		privateReqCancel()
		c.logger.Info("connection accepted")
		return conn, eg.Wait()
	case conn := <-publicDialled:
		acceptCancel()
		privateReqCancel()
		c.logger.Info("connection established via public dial")
		return conn, eg.Wait()
	case conn := <-privateDialled:
		acceptCancel()
		publicReqCancel()
		c.logger.Info("connection established via private dial")
		return conn, eg.Wait()
	case <-ctx.Done():
		acceptCancel()
		publicReqCancel()
		privateReqCancel()
		return nil, eg.Wait()
	}
}

func (c *Connector) dial(ctx context.Context, addr netip.AddrPort) (*quic.Conn, error) {
	conn, err := c.transport.Dial(ctx, net.UDPAddrFromAddrPort(addr), c.tlsConf, c.quicConf)
	if err != nil {
		return nil, fmt.Errorf("dial QUIC: %w", err)
	}

	return conn, nil
}

func (c *Connector) sendRequest(ctx context.Context, conn *quic.Conn) error {
	req, err := c.newRequest(ctx)
	if err != nil {
		return err
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer func() {
		stream.CancelRead(quic.StreamErrorCode(quic.NoError))
		stream.Close()
	}()

	if err := req.Write(stream); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		if isConflictError(err) {
			return errConflict
		}

		return fmt.Errorf("read response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return httpplatform.NewBadStatusCodeError(resp.StatusCode, resp.Body)
	}

	return nil
}

func isConflictError(err error) bool {
	var appErr *quic.ApplicationError
	if errors.As(err, &appErr) {
		return appErr.Remote && appErr.ErrorCode == errcodeConflict
	}

	return false
}

func (c *Connector) newRequest(ctx context.Context) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "/connect", http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Connector-ID", c.id)
	return req, nil
}

// acceptConnection returns true is connection was successfully established,
// false if connection was rejected.
func (c *Connector) acceptConnection(ctx context.Context, conn *quic.Conn) (bool, error) {
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return false, fmt.Errorf("accept stream: %w", err)
	}

	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		return false, fmt.Errorf("read request: %w", err)
	}

	peerID := req.Header.Get("X-Connector-ID")
	if peerID == "" {
		return false, conn.CloseWithError(
			errcodeBadRequest,
			"missing X-Connector-ID",
		)
	}

	if peerID > c.id {
		c.logger.Debug("tie-breaking: peer has larger ID, rejecting connection", "our_id", c.id, "peer_id", peerID)
		return false, conn.CloseWithError(
			errcodeConflict,
			"connection rejected due to tie-breaking",
		)
	}
	c.logger.Debug("tie-breaking: accepting connection", "peer_id", peerID)

	httpWrite(stream, http.NoBody, http.StatusOK)

	stream.CancelRead(quic.StreamErrorCode(quic.NoError))
	stream.Close()

	return true, nil
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
