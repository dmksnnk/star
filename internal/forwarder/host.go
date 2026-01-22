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
	"sync"

	"github.com/dmksnnk/star/internal/discovery"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
)

var (
	errcodeFailedToConnect = quic.ApplicationErrorCode(0x1001)
	errcodeLinkFailed      = quic.ApplicationErrorCode(0x1002)
)

type HostConfig struct {
	Logger      *slog.Logger
	TLSConfig   *tls.Config
	ErrHandlers []func(error)
}

func (h HostConfig) Register(
	ctx context.Context,
	tr *quic.Transport, // transport with discoverder connection
	baseURL *url.URL,
	publicAddr netip.AddrPort,
	token auth.Token,
) (*Host, error) {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("role", "host")

	client, err := registar.RegisterClient(ctx, tr, h.TLSConfig, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("register registar client: %w", err)
	}

	hostAddrs := registar.AddrPair{
		Public:  publicAddr,
		Private: tr.Conn.LocalAddr().(*net.UDPAddr).AddrPort(),
	}

	stream, err := client.Host(ctx, hostAddrs)
	if err != nil {
		return nil, fmt.Errorf("create host stream: %w", err)
	}

	controlListener := ListenControl(stream, tr, client.TLSConfig())

	return &Host{
		transport:   tr,
		client:      client,
		control:     controlListener,
		logger:      logger,
		errHandlers: h.ErrHandlers,
	}, nil
}

type Host struct {
	transport *quic.Transport
	client    *registar.RegisteredClient
	control   *ControlListener
	wg        sync.WaitGroup

	logger      *slog.Logger
	errHandlers []func(error)
}

func (h *Host) Run(ctx context.Context, gamePort int) error {
	for {
		peerConn, err := h.control.Accept(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}

			return fmt.Errorf("accept peer connection: %w", err)
		}

		h.logger.Debug("accepted peer connection", "addr", peerConn.RemoteAddr())

		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer peerConn.CloseWithError(0, "closing connection")
			defer h.logger.Debug("link finished")

			if err := linkWithHost(ctx, peerConn, gamePort); err != nil {
				h.logger.Error("link with host failed", "error", err)
				h.handleError(err)
			}
		}()
	}
}

func (h *Host) Close() error {
	h.control.Close()
	h.wg.Wait()

	// must close client, otherwise it holds connection to the server and sever cannot shut down
	h.client.Close()
	return h.transport.Close()
}

func (h *Host) handleError(err error) {
	for _, handler := range h.errHandlers {
		handler(err)
	}
}

// link links incoming QUIC connections from peers to the local game UDP port.
func linkWithHost(ctx context.Context, conn *quic.Conn, gamePort int) error {
	gameConn, err := net.DialUDP("udp", nil, &net.UDPAddr{Port: gamePort})
	if err != nil {
		conn.CloseWithError(errcodeFailedToConnect, "dial game UDP failed")
		return fmt.Errorf("dial game UDP: %w", err)
	}
	defer gameConn.Close()

	if err := link(ctx, gameConn, conn); err != nil {
		conn.CloseWithError(errcodeLinkFailed, "link with game failed")
		return fmt.Errorf("link with game UDP: %w", err)
	}

	return nil
}

// DiscoverConn creates a UDP connection and discovers its public address using the discovery server.
func DiscoverConn(addr *net.UDPAddr, discoverySrv netip.AddrPort) (*net.UDPConn, netip.AddrPort, error) {
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, netip.AddrPort{}, fmt.Errorf("listen UDP: %w", err)
	}

	publicAddr, err := discovery.Bind(conn, discoverySrv)
	if err != nil {
		return nil, netip.AddrPort{}, fmt.Errorf("discovery public address: %w", err)
	}

	return conn, publicAddr, nil
}
