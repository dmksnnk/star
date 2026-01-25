package forwarder

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"

	"github.com/dmksnnk/star/internal/platform/udp"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
)

type PeerConfig struct {
	// Logger specifies an optional logger.
	// If nil, [slog.Default] will be used.
	Logger *slog.Logger
	// TLS config for calling registar.
	// For tests it should should trust registar's cert
	TLSConfig *tls.Config
	// RegistarListenAddr specifies the local UDP address to listen on for incoming QUIC connections.
	// If the IP field is nil or an unspecified IP address,
	// PeerConfig listens on all available IP addresses of the local system
	// except multicast IP addresses.
	// If the Port is 0, a port number is automatically
	// chosen.
	RegistarListenAddr *net.UDPAddr
	// GameListenPort specifies the local UDP port to listen on for incoming game connections.
	// If 0, a port number is automatically chosen.
	GameListenPort int
	// ConfigureTransport is an optional callback that is called to configure the QUIC transport
	// before it is used.
	ConfigureTransport func(*quic.Transport)
}

func (p PeerConfig) Register(
	ctx context.Context,
	discoverySrv netip.AddrPort,
	baseURL *url.URL,
	token auth.Token,
) (*Peer, error) {
	conn, localAddr, publicAddr, err := DiscoverConn(ctx, p.RegistarListenAddr, discoverySrv)
	if err != nil {
		return nil, fmt.Errorf("discover connection: %w", err)
	}

	p.Logger.DebugContext(ctx, "discovered addresses", slog.String("local", localAddr.String()), slog.String("public", publicAddr.String()))

	tr := &quic.Transport{
		Conn: conn,
	}
	if p.ConfigureTransport != nil {
		p.ConfigureTransport(tr)
	}

	client, err := registar.RegisterClient(ctx, tr, p.TLSConfig, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("register registar client: %w", err)
	}

	peerAddrs := registar.AddrPair{
		Public:  publicAddr,
		Private: localAddr,
	}
	controlStream, err := client.Join(ctx, peerAddrs)
	if err != nil {
		return nil, fmt.Errorf("create peer stream: %w", err)
	}

	gameListener, err := udp.Listen(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p.GameListenPort})
	if err != nil {
		return nil, fmt.Errorf("listen UDP: %w", err)
	}

	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}

	clc := ControlListenerConfig{
		Logger: logger.With(slog.String("component", "control_listener")),
	}

	return &Peer{
		transport:    tr,
		client:       client,
		control:      clc.ListenControl(controlStream, tr, client.TLSConfig()),
		gameListener: gameListener,
		logger:       logger,
	}, nil
}

type Peer struct {
	transport    *quic.Transport
	client       *registar.RegisteredClient
	control      *ControlListener
	gameListener *udp.Listener

	gameConn net.Conn
	hostConn *quic.Conn

	logger *slog.Logger
}

// UDPAddr returns the address the peer is listening on for game connections.
func (p *Peer) UDPAddr() *net.UDPAddr {
	return p.gameListener.UDPAddr()
}

func (p *Peer) AcceptAndLink(ctx context.Context) error { // TODO: handle reconnects
	hostConn, err := p.control.Accept(ctx)
	if err != nil {
		return fmt.Errorf("accept peer connection: %w", err)
	}

	p.logger.Debug("accepted host connection", "addr", hostConn.RemoteAddr())

	gameConn, err := p.gameListener.Accept()
	if err != nil {
		return fmt.Errorf("accept incoming game connection: %w", err)
	}
	defer gameConn.Close()

	p.logger.Debug("accepted game connection", "addr", gameConn.RemoteAddr())

	if err := link(ctx, gameConn, hostConn); err != nil {
		// TODO: meaningful error codes
		hostConn.CloseWithError(0, "link with game failed")

		// Close() called
		if errors.Is(err, quic.ErrTransportClosed) {
			return nil
		}

		return fmt.Errorf("link with game: %w", err)
	}

	return hostConn.CloseWithError(0, "peer exited")
}

func (p *Peer) Close() error {
	return errors.Join(
		p.gameListener.Close(),
		p.control.Close(),
		// must close client, otherwise it holds connection to the server and sever cannot shut down
		p.client.Close(),
		// close transport last, otherwise it hangs waiting for client connection
		p.transport.Close(),
	)
}
