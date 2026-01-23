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
	// ConfigureTransport is an optional callback that is called to configure the QUIC transport
	// before it is used.
	ConfigureTransport func(*quic.Transport)
	// ErrHandlers are called when an error occurs when handling links.
	ErrHandlers []func(error)
}

func (h HostConfig) Register(
	ctx context.Context,
	discoverySrv netip.AddrPort,
	baseURL *url.URL,
	token auth.Token,
) (*Host, error) {
	conn, publicAddr, err := DiscoverConn(ctx, h.ListenAddr, discoverySrv)
	if err != nil {
		return nil, fmt.Errorf("discover connection: %w", err)
	}

	tr := &quic.Transport{
		Conn: conn,
	}
	if h.ConfigureTransport != nil {
		h.ConfigureTransport(tr)
	}

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

	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("role", "host")

	return &Host{
		transport:   tr,
		client:      client,
		control:     ListenControl(stream, tr, client.TLSConfig()),
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

func (h *Host) Run(ctx context.Context, gameAddr *net.UDPAddr) error {
	for {
		peerConn, err := h.control.Accept(ctx)
		if err != nil {
			if errors.Is(err, errControlListenerClosed) {
				return nil
			}

			return fmt.Errorf("accept peer connection: %w", err)
		}

		h.logger.Debug("accepted peer connection", "addr", peerConn.RemoteAddr())

		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer peerConn.CloseWithError(0, "closing connection")
			defer h.logger.Debug("link finished", "addr", peerConn.RemoteAddr())

			if err := linkWithHost(ctx, peerConn, gameAddr); err != nil {
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
		// must close client, otherwise it holds connection to the server and sever cannot shut down
		h.client.Close(),
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
func linkWithHost(ctx context.Context, conn *quic.Conn, gameAddr *net.UDPAddr) error {
	gameConn, err := net.DialUDP("udp", nil, gameAddr)
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
func DiscoverConn(ctx context.Context, addr *net.UDPAddr, discoverySrv netip.AddrPort) (*net.UDPConn, netip.AddrPort, error) {
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, netip.AddrPort{}, fmt.Errorf("listen UDP: %w", err)
	}

	publicAddr, err := discovery.Bind(ctx, conn, discoverySrv)
	if err != nil {
		return nil, netip.AddrPort{}, fmt.Errorf("discovery public address: %w", err)
	}

	return conn, publicAddr, nil
}
