package forwarder

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sync"

	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
)

type HostConfig struct {
	// Logger specifies an optional logger.
	// If nil, [slog.Default] will be used.
	Logger *slog.Logger
	// TLS config for calling registar.
	// For tests it should should trust registar's cert
	TLSConfig *tls.Config
	// ListenAddr specifies the local UDP address to listen on for incoming QUIC connections.
	// If the IP field is nil or an unspecified IP address,
	// HostConfig listens on all available IP addresses of the local system
	// except multicast IP addresses.
	// If the Port field is 0, a port number is automatically
	// chosen.
	ListenAddr *net.UDPAddr
	// ErrHandlers are called when an error occurs when handling links.
	ErrHandlers []func(error)
}

func (h HostConfig) Register(
	ctx context.Context,
	baseURL *url.URL,
	token auth.Token,
) (*Host, error) {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}

	cc := registar.ClientConfig{
		TLSConfig:  h.TLSConfig.Clone(),
		ListenAddr: h.ListenAddr,
		Logger:     logger,
	}

	hoster, err := cc.NewHoster(ctx, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("create hoster: %w", err)
	}

	return &Host{
		hoster:      hoster,
		linkConfig:  linkConfig{UDPIdleTimeout: 0}, // never terminate connection to the game, as we are the host
		logger:      logger,
		errHandlers: h.ErrHandlers,
	}, nil
}

type Host struct {
	hoster     *registar.Hoster
	linkConfig linkConfig
	wg         sync.WaitGroup

	logger      *slog.Logger
	errHandlers []func(error)
}

// AcceptAndLink accepts incoming peer connections and links them to the local UDP game.
func (h *Host) AcceptAndLink(ctx context.Context, gameAddr *net.UDPAddr) error {
	for {
		peerConn, err := h.hoster.Accept(ctx)
		if err != nil {
			if errors.Is(err, registar.ErrHosterClosed) {
				return nil
			}

			return fmt.Errorf("accept peer connection: %w", err)
		}

		h.logger.Debug("accepted peer connection")

		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer peerConn.Close()
			defer h.logger.Debug("link finished")

			if err := linkWithHost(ctx, peerConn, gameAddr, h.linkConfig); err != nil {
				// Close() called
				if errors.Is(err, quic.ErrTransportClosed) {
					return
				}
				// context cancelled
				if errors.Is(err, context.Canceled) {
					return
				}

				h.handleError(err)
			}
		}()
	}
}

func (h *Host) Close() error {
	err := errors.Join(
		h.hoster.Close(),
	)

	h.wg.Wait()

	return err
}

func (h *Host) handleError(err error) {
	for _, handler := range h.errHandlers {
		handler(err)
	}
}

// linkWithHost links incoming QUIC connections from peers to the local UDP game.
func linkWithHost(ctx context.Context, conn registar.DatagramConn, gameAddr *net.UDPAddr, lc linkConfig) error {
	gameConn, err := net.DialUDP("udp", nil, gameAddr)
	if err != nil {
		return fmt.Errorf("dial game UDP: %w", err)
	}
	defer gameConn.Close()

	if err := lc.link(ctx, gameConn, conn); err != nil {
		return fmt.Errorf("link with game UDP: %w", err)
	}

	return nil
}
