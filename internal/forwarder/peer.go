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
	// ListenAddr specifies the local UDP address to listen on for incoming QUIC connections.
	// If the IP field is nil or an unspecified IP address,
	// PeerConfig listens on all available IP addresses of the local system
	// except multicast IP addresses.
	// If the Port is 0, a port number is automatically
	// chosen.
	ListenAddr *net.UDPAddr
	// GameListenPort specifies the local UDP port to listen on for incoming game connections.
	// If 0, a port number is automatically chosen.
	GameListenPort int
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
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}

	cc := registar.ClientConfig{
		TLSConfig:  p.TLSConfig.Clone(),
		ListenAddr: p.ListenAddr,
		Logger:     logger,
	}

	joiner, err := cc.NewJoiner(ctx, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("create joiner: %w", err)
	}

	hostConn, err := joiner.Join(ctx)
	if err != nil {
		joiner.Close()
		return nil, fmt.Errorf("join host: %w", err)
	}

	lc := udp.ListenConfig{
		Logger: logger.With(slog.String("component", "udp.ListenConfig")),
	}
	gameListener, err := lc.Listen(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p.GameListenPort})
	if err != nil {
		joiner.Close()
		return nil, fmt.Errorf("listen UDP: %w", err)
	}

	udpIdleTimeout := p.UDPIdleTimeout
	if udpIdleTimeout == 0 {
		udpIdleTimeout = defaultUDPIdleTimeout
	}

	return &Peer{
		joiner:       joiner,
		hostConn:     hostConn,
		gameListener: gameListener,
		linkConfig:   linkConfig{UDPIdleTimeout: udpIdleTimeout},
		logger:       logger,
	}, nil
}

type Peer struct {
	joiner       *registar.Joiner
	hostConn     registar.DatagramConn
	gameListener *udp.Listener
	linkConfig   linkConfig

	logger *slog.Logger
}

// UDPAddr returns the address the peer is listening on for game connections.
func (p *Peer) UDPAddr() *net.UDPAddr {
	return p.gameListener.UDPAddr()
}

// AcceptAndLink accepts incoming game connections and links them to the host peer connection.
// It re-links game connection to the host connection if it is disconnected.
func (p *Peer) AcceptAndLink(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() {
		p.gameListener.Close()
	})
	defer stop()

	for {
		gameConn, err := p.gameListener.Accept()
		if err != nil {
			if errors.Is(err, udp.ErrClosedListener) && ctx.Err() != nil {
				return context.Canceled // context canceled
			}

			return fmt.Errorf("accept incoming game connection: %w", err)
		}

		p.logger.Debug("accepted game connection", "addr", gameConn.RemoteAddr())

		if err := p.linkConfig.link(ctx, gameConn, p.hostConn); err != nil {
			if errors.Is(err, errLinkIdleTimeout) {
				p.logger.Debug("link idle timeout, closing game connection")
				gameConn.Close()
				p.hostConn, err = p.joiner.Join(ctx)
				if err != nil {
					return fmt.Errorf("join host: %w", err)
				}
				continue
			}

			gameConn.Close()

			// Close() called
			if errors.Is(err, quic.ErrTransportClosed) {
				return nil
			}

			p.hostConn.Close()
			return fmt.Errorf("link with game: %w", err)
		}
	}
}

func (p *Peer) Close() error {
	return errors.Join(
		p.gameListener.Close(),
		p.joiner.Close(),
	)
}
