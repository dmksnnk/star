package control

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
	"strings"

	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

var (
	errConnectionRejected  = errors.New("connection rejected")
	errMissingConnectionID = errors.New("missing connection ID")
)

const (
	errcodeCancelled quic.ApplicationErrorCode = iota + 1
	errcodeMissingConnectionID
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

func (c *Connector) Connect(ctx context.Context, public, private string) (http3platform.Stream, error) {
	earlyListener, err := c.transport.ListenEarly(c.tlsConf, c.quicConf)
	if err != nil {
		return nil, fmt.Errorf("listen early: %w", err)
	}
	defer earlyListener.Close()

	eg, ctx := errgroup.WithContext(ctx)

	accepted := make(chan *Stream)
	acceptCtx, acceptCancel := context.WithCancel(ctx)
	defer acceptCancel()
	eg.Go(func() error {
		for {
			conn, err := earlyListener.Accept(acceptCtx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
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

			stream, err := c.handleRequest(ctx, conn)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}

				return nil
			}

			select {
			case accepted <- &Stream{conn, stream}:
				c.logger.Debug("accepted connection", "remote_addr", conn.RemoteAddr().String())
				return nil
			case <-acceptCtx.Done():
				err := fmt.Errorf("server connection cancelled: %w", context.Cause(acceptCtx))
				return conn.CloseWithError(errcodeCancelled, err.Error())
			}
		}
	})

	publicDialled := make(chan *Stream)
	publicReqCtx, publicReqCancel := context.WithCancel(ctx)
	defer publicReqCancel()
	eg.Go(func() error {
		for {
			conn, err := c.dial(publicReqCtx, public)
			if err != nil {
				// if isHandshakeTimeoutError(err) {
				// 	continue
				// }
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

			stream, err := c.sendRequest(publicReqCtx, conn)
			if err != nil {
				// if isHandshakeTimeoutError(err) {
				// 	continue
				// }

				if errors.Is(err, errConnectionRejected) {
					c.logger.Debug("dial public address rejected by peer, retrying")
					conn.CloseWithError(errcodeCancelled, "closing after rejection")
					continue
				}
				if errors.Is(err, context.Canceled) {
					err := fmt.Errorf("public connection cancelled: %w", context.Cause(publicReqCtx))
					return conn.CloseWithError(errcodeCancelled, err.Error())
				}

				return fmt.Errorf("send request to public address: %w", err)
			}

			select {
			case publicDialled <- &Stream{conn, stream}:
				c.logger.Debug("established connection to public adddress", "remote_addr", conn.RemoteAddr().String())
				return nil
			case <-publicReqCtx.Done():
				err := fmt.Errorf("public connection cancelled: %w", context.Cause(publicReqCtx))
				return conn.CloseWithError(errcodeCancelled, err.Error())
			}
		}
	})

	privateDialled := make(chan *Stream)
	privateReqCtx, privateReqCancel := context.WithCancel(ctx)
	defer privateReqCancel()
	eg.Go(func() error {
		for {
			conn, err := c.dial(privateReqCtx, private)
			if err != nil {
				// if isHandshakeTimeoutError(err) {
				// 	continue
				// }
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

			stream, err := c.sendRequest(privateReqCtx, conn)
			if err != nil {
				// if isHandshakeTimeoutError(err) {
				// continue
				// }
				if errors.Is(err, errConnectionRejected) {
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
			case privateDialled <- &Stream{conn, stream}:
				c.logger.Debug("established connetion to private address", "remote_addr", conn.RemoteAddr().String())
				return nil
			case <-privateReqCtx.Done():
				err := fmt.Errorf("private connection cancelled: %w", context.Cause(privateReqCtx))
				return conn.CloseWithError(errcodeCancelled, err.Error())
			}
		}
	})

	select {
	case stream := <-accepted:
		publicReqCancel()
		privateReqCancel()
		c.logger.Info("connection established via server")
		return stream, eg.Wait()
	case stream := <-publicDialled:
		acceptCancel()
		privateReqCancel()
		c.logger.Info("connection established via public dial")
		return stream, eg.Wait()
	case stream := <-privateDialled:
		acceptCancel()
		publicReqCancel()
		c.logger.Info("connection established via private dial")
		return stream, eg.Wait()
	case <-ctx.Done():
		acceptCancel()
		publicReqCancel()
		privateReqCancel()
		return nil, eg.Wait()
	}
}

func (c *Connector) dial(ctx context.Context, addr string) (*quic.Conn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP address: %w", err)
	}

	conn, err := c.transport.Dial(ctx, udpAddr, c.tlsConf, c.quicConf)
	if err != nil {
		return nil, fmt.Errorf("dial QUIC: %w", err)
	}

	return conn, nil
}

func (c *Connector) sendRequest(ctx context.Context, conn *quic.Conn) (*quic.Stream, error) {
	req, err := c.newRequest(ctx)
	if err != nil {
		return nil, err
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}

	if err := req.Write(stream); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusConflict {
			return nil, errConnectionRejected
		}

		return nil, httpplatform.NewBadStatusCodeError(resp.StatusCode, resp.Body)
	}

	return stream, nil
}

func (c *Connector) newRequest(ctx context.Context) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "/connect", http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Connector-ID", c.id)
	return req, nil
}

func (c *Connector) handleRequest(ctx context.Context, conn *quic.Conn) (*quic.Stream, error) {
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return nil, fmt.Errorf("accept stream: %w", err)
		}

		req, err := http.ReadRequest(bufio.NewReader(stream))
		if err != nil {
			return nil, fmt.Errorf("read request: %w", err)
		}

		peerID := req.Header.Get("X-Connector-ID")
		if peerID == "" {
			httpError(stream, "missing X-Connector-ID", http.StatusBadRequest)
			stream.CancelRead(0)
			continue
		}

		if peerID > c.id {
			c.logger.Debug("tie-breaking: peer has larger ID, rejecting connection", "our_id", c.id, "peer_id", peerID)
			httpError(stream, "rejected by tie-breaker", http.StatusConflict)
			stream.CancelRead(0)
			continue
		}
		c.logger.Debug("tie-breaking: we have larger or equal ID, accepting connection", "peer_id", peerID)

		httpWrite(stream, http.NoBody, http.StatusOK)

		return stream, nil
	}
}

func isHandshakeTimeoutError(err error) bool {
	var toErr *quic.HandshakeTimeoutError
	return errors.As(err, &toErr) && toErr.Timeout()
}

func httpError(w io.Writer, error string, status int) {
	httpWrite(w, io.NopCloser(strings.NewReader(error)), status)
}

func httpWrite(w io.Writer, body io.ReadCloser, status int) {
	r := http.Response{
		StatusCode: status,
		Body:       body,
	}

	r.Write(w)
}

var _ http3platform.Stream = &Stream{}

type Stream struct {
	*quic.Conn
	*quic.Stream
}

func (s *Stream) Context() context.Context {
	return s.Stream.Context()
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
