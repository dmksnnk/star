package forwarder

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"

	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
)

type PeerConfig struct {
	Logger    *slog.Logger
	TLSConfig *tls.Config // TLS config for registar client, should trust registar's cert
}

func (p PeerConfig) Register(
	ctx context.Context,
	tr *quic.Transport, // transport with discoverder connection
	baseURL *url.URL,
	publicAddr netip.AddrPort,
	token auth.Token,
) (Peer, error) {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("role", "peer")

	client, err := registar.RegisterClient(ctx, tr, p.TLSConfig, baseURL, token)
	if err != nil {
		return Peer{}, fmt.Errorf("register registar client: %w", err)
	}

	peerAddrs := registar.AddrPair{
		Public:  publicAddr,
		Private: tr.Conn.LocalAddr().(*net.UDPAddr).AddrPort(),
	}

	controlStream, err := client.Join(ctx, peerAddrs)
	if err != nil {
		return Peer{}, fmt.Errorf("create peer stream: %w", err)
	}

	controlListener := ListenControl(controlStream, tr, client.TLSConfig())

	return Peer{
		client:  client,
		control: controlListener,
		logger:  logger,
	}, nil
}

type Peer struct {
	client  *registar.RegisteredClient
	control *ControlListener
	logger  *slog.Logger
}

func (p *Peer) AcceptAndLink(ctx context.Context, l net.Listener) error { // TODO: handle reconnects
	gameConn, err := l.Accept()
	if err != nil {
		return fmt.Errorf("accept incoming game connection: %w", err)
	}
	defer gameConn.Close()

	p.logger.Debug("accepted game connection", "addr", gameConn.RemoteAddr())

	hostConn, err := p.control.Accept(ctx)
	if err != nil {
		return fmt.Errorf("accept peer connection: %w", err)
	}

	p.logger.Debug("accepted host connection", "addr", hostConn.RemoteAddr())

	if err := link(ctx, gameConn, hostConn); err != nil {
		hostConn.CloseWithError(0, "link with game failed")
		return fmt.Errorf("link with game: %w", err)
	}

	return hostConn.CloseWithError(0, "peer exited")
}

func (p Peer) Close() error {
	p.control.Close()
	// must close client, otherwise it holds connection to the server and sever cannot shut down
	return p.client.Close()
}
