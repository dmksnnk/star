package forwarder

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/p2p"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

var errcodeClosed = quic.ApplicationErrorCode(0x1003)

var errControlListenerClosed = errors.New("control listener closed")

type ControlListenerConfig struct {
	Logger *slog.Logger
	// P2PDialTimeout sets the maximum idle timeout for P2P connection attempts.
	// A shorter value makes hole-punching fail faster when the remote is unreachable.
	// If zero, the quic-go default applies (~5s for the handshake phase).
	P2PDialTimeout time.Duration
}

func (c ControlListenerConfig) ListenControl(controlConn *quic.Conn, tr *quic.Transport, tlsConf *tls.Config) *ControlListener {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}

	connectorOpts := []p2p.Option{
		p2p.WithLogger(logger.With(slog.String("component", "p2p.Connector"))),
	}
	if c.P2PDialTimeout > 0 {
		connectorOpts = append(connectorOpts, p2p.WithHandshakeIdleTimeout(c.P2PDialTimeout))
	}
	connector := p2p.NewConnector(tr, tlsConf, connectorOpts...)
	agent := control.NewAgent()
	conns := make(chan *quic.Conn, 1)

	agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		p2pConn, err := connector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			var idleErr *quic.IdleTimeoutError
			if errors.As(err, &idleErr) {
				logger.DebugContext(ctx, "peer connection timed out", "public_addr", cmd.PublicAddress, "private_addr", cmd.PrivateAddress)
				return false, nil
			}

			logger.ErrorContext(ctx, "p2p connection failed", "error", err)
			return false, fmt.Errorf("connect to peer: %w", err)
		}

		select {
		case conns <- p2pConn:
			logger.DebugContext(ctx, "established p2p connection", slog.String("remote_addr", p2pConn.RemoteAddr().String()))
			return true, nil
		case <-ctx.Done():
			// TODO: better error code
			p2pConn.CloseWithError(0, "rejected: context cancelled")
			return false, ctx.Err()
		}
	})

	var eg errgroup.Group
	eg.Go(func() error {
		return agent.Serve(controlConn)
	})

	return &ControlListener{
		eg:       &eg,
		ctrlConn: controlConn,
		conns:    conns,
		done:     make(chan struct{}),
	}
}

type ControlListener struct {
	eg       *errgroup.Group
	ctrlConn *quic.Conn
	conns    chan *quic.Conn
	done     chan struct{}
}

// Accept waits for and returns the next incoming peer connection.
// Returns errControlListenerClosed when listener is closed.
func (l *ControlListener) Accept(ctx context.Context) (*quic.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, errControlListenerClosed
	case <-l.ctrlConn.Context().Done():
		return nil, errControlListenerClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the control stream and waits for all underlying goroutines to exit.
// Accept will immediately unblock and return errControlListenerClosed.
func (l *ControlListener) Close() error {
	l.ctrlConn.CloseWithError(errcodeClosed, "control listener closed")
	close(l.done)

	if err := l.eg.Wait(); err != nil {
		if isLocalCloseError(err) {
			return nil
		}

		return fmt.Errorf("control listener error: %w", err)
	}

	return nil
}

func isLocalCloseError(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) &&
		appErr.ErrorCode == errcodeClosed &&
		!appErr.Remote
}
