package forwarder

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"

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
	// UDPIdleTimeout is the maximum duration without receiving UDP data
	// from the game before the game connection is considered idle and terminated.
	// If zero, defaults to 10s.
	UDPIdleTimeout time.Duration
}

func (p PeerConfig) Join(
	ctx context.Context,
	baseURL *url.URL,
	token auth.Token,
) (*Peer, error) {
	conn, err := net.ListenUDP("udp", p.RegistarListenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen UDP: %w", err)
	}

	tr := &quic.Transport{
		Conn: conn,
	}
	if p.ConfigureTransport != nil {
		p.ConfigureTransport(tr)
	}

	cc := registar.ClientConfig{
		QUICConfig: &quic.Config{
			KeepAlivePeriod: 10 * time.Second, // need keep-alive so connection does not close
		},
		TLSConfig: p.TLSConfig.Clone(),
	}
	ctrlConn, p2pTLSConf, err := cc.Join(ctx, tr, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("join p2p: %w", err)
	}

	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}

	lc := udp.ListenConfig{
		Logger: logger.With(slog.String("component", "udp.ListenConfig")),
	}
	gameListener, err := lc.Listen(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p.GameListenPort})
	if err != nil {
		return nil, fmt.Errorf("listen UDP: %w", err)
	}

	clc := ControlListenerConfig{
		Logger: logger.With(slog.String("component", "ControlListener")),
	}

	udpIdleTimeout := p.UDPIdleTimeout
	if udpIdleTimeout == 0 {
		udpIdleTimeout = defaultUDPIdleTimeout
	}

	return &Peer{
		transport:    tr,
		control:      clc.ListenControl(ctrlConn, tr, p2pTLSConf),
		gameListener: gameListener,
		linkConfig:   linkConfig{UDPIdleTimeout: udpIdleTimeout},
		logger:       logger,
	}, nil
}

type Peer struct {
	transport    *quic.Transport
	control      *ControlListener
	gameListener *udp.Listener
	linkConfig   linkConfig

	logger *slog.Logger
}

// UDPAddr returns the address the peer is listening on for game connections.
func (p *Peer) UDPAddr() *net.UDPAddr {
	return p.gameListener.UDPAddr()
}

func (p *Peer) AcceptAndLink(ctx context.Context) error {
	hostConn, err := p.control.Accept(ctx)
	if err != nil {
		return fmt.Errorf("accept peer connection: %w", err)
	}

	p.logger.Debug("accepted host connection", "remote_addr", hostConn.RemoteAddr().String())

	for {
		gameConn, err := p.gameListener.Accept()
		if err != nil {
			return fmt.Errorf("accept incoming game connection: %w", err)
		}

		p.logger.Debug("accepted game connection", "addr", gameConn.RemoteAddr())

		if err := p.linkConfig.link(ctx, gameConn, hostConn); err != nil {
			if errors.Is(err, errLinkIdleTimeout) {
				p.logger.Debug("link idle timeout, closing game connection")
				gameConn.Close()
				continue
			}

			gameConn.Close()

			// Close() called
			if errors.Is(err, quic.ErrTransportClosed) {
				hostConn.CloseWithError(0, "closing host connection")
				return nil
			}

			// TODO: meaningful error codes
			hostConn.CloseWithError(0, fmt.Sprintf("link with game failed: %s", err))
			return fmt.Errorf("link with game: %w", err)
		}
	}
}

func (p *Peer) Close() error {
	return errors.Join(
		p.gameListener.Close(),
		p.control.Close(),
		// close transport last, otherwise it hangs waiting for client connection
		p.transport.Close(),
	)
}
