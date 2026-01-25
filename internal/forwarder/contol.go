package forwarder

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/p2p"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

var errcodeClosed = quic.StreamErrorCode(0x1003)

var errControlListenerClosed = errors.New("control listener closed")

type ControlListenerConfig struct {
	Logger *slog.Logger
}

func (c ControlListenerConfig) ListenControl(ctrlStream *http3.RequestStream, tr *quic.Transport, tlsConf *tls.Config) *ControlListener {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}

	connector := p2p.NewConnector(tr, tlsConf, p2p.WithLogger(logger.With(slog.String("component", "p2p.Connector"))))
	agent := control.NewAgent()
	conns := make(chan *quic.Conn, 1)

	var eg errgroup.Group

	agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		p2pConn, err := connector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			logger.ErrorContext(ctx, "p2p connection failed", "error", err)
			// TODO: figure out on which error return just false without error
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

	eg.Go(func() error {
		return agent.Serve(ctrlStream)
	})

	return &ControlListener{
		eg:         &eg,
		ctrlStream: ctrlStream,
		conns:      conns,
		done:       make(chan struct{}),
	}
}

type ControlListener struct {
	eg         *errgroup.Group
	ctrlStream *http3.RequestStream
	conns      chan *quic.Conn
	done       chan struct{}
}

// Accept waits for and returns the next incoming peer connection.
// Returns errControlListenerClosed when listener is closed.
func (l *ControlListener) Accept(ctx context.Context) (*quic.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, errControlListenerClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the control stream and waits for all underlying goroutines to exit.
// Accept will immediately unblock and return errControlListenerClosed.
func (l *ControlListener) Close() error {
	l.ctrlStream.CancelRead(errcodeClosed)
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
	var h3Err *http3.Error
	return errors.As(err, &h3Err) &&
		h3Err.ErrorCode == http3.ErrCode(errcodeClosed) &&
		!h3Err.Remote
}
