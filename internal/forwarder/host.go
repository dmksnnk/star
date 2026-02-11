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

var (
	errcodeFailedToConnect = quic.ApplicationErrorCode(0x1001)
	errcodeLinkFailed      = quic.ApplicationErrorCode(0x1002)
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
	conn, err := net.ListenUDP("udp", h.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen UDP: %w", err)
	}

	tr := &quic.Transport{
		Conn: conn,
	}
	cc := registar.ClientConfig{
		TLSConfig: h.TLSConfig.Clone(),
	}
	ctrlConn, p2pTLSConf, err := cc.Host(ctx, tr, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("host p2p: %w", err)
	}

	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("role", "host")

	clc := ControlListenerConfig{
		Logger: logger.With(slog.String("component", "ControlListener")),
	}

	return &Host{
		transport:   tr,
		control:     clc.ListenControl(ctrlConn, tr, p2pTLSConf),
		linkConfig:  linkConfig{UDPIdleTimeout: 0}, // never terminate connection to the game, as we are the host
		logger:      logger,
		errHandlers: h.ErrHandlers,
	}, nil
}

type Host struct {
	transport  *quic.Transport
	control    *ControlListener
	linkConfig linkConfig
	wg         sync.WaitGroup

	logger      *slog.Logger
	errHandlers []func(error)
}

// AcceptAndLink accepts incoming peer connections and links them to the local UDP game.
func (h *Host) AcceptAndLink(ctx context.Context, gameAddr *net.UDPAddr) error {
	for {
		peerConn, err := h.control.Accept(ctx)
		if err != nil {
			if errors.Is(err, errControlListenerClosed) {
				return nil
			}

			return fmt.Errorf("accept peer connection: %w", err)
		}

		h.logger.Debug("accepted peer connection", "remote_addr", peerConn.RemoteAddr().String())

		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer peerConn.CloseWithError(0, "closing connection")
			defer h.logger.Debug("link finished", "remote_addr", peerConn.RemoteAddr().String())

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
		h.control.Close(),
		// close transport last, otherwise it hangs waiting for client connection
		h.transport.Close(),
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
func linkWithHost(ctx context.Context, conn *quic.Conn, gameAddr *net.UDPAddr, lc linkConfig) error {
	gameConn, err := net.DialUDP("udp", nil, gameAddr)
	if err != nil {
		conn.CloseWithError(errcodeFailedToConnect, "dial game UDP failed")
		return fmt.Errorf("dial game UDP: %w", err)
	}
	defer gameConn.Close()

	if err := lc.link(ctx, gameConn, conn); err != nil {
		conn.CloseWithError(errcodeLinkFailed, fmt.Sprintf("link with game failed: %s", err))
		return fmt.Errorf("link with game UDP: %w", err)
	}

	return nil
}
