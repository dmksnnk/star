package forwarder

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/dmksnnk/star/internal/platform/udp"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go/http3"
)

// HostConnector allows to connect to a host identified by a key.
type HostConnector interface {
	ConnectToHost(ctx context.Context, key auth.Key, id string) (*http3.RequestStream, error)
}

// Peer is a connection peer that connects
type Peer struct {
	connector HostConnector
	listener  net.Listener
	logger    *slog.Logger

	mux   sync.Mutex
	links map[*link]struct{}
}

// PeerListenLocalUDP creates a new Peer that listens for incoming connections on a local UDP address.
func PeerListenLocalUDP(ctx context.Context, connector HostConnector, ops ...PeerOption) (*Peer, error) {
	localhost := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0,
	}
	llc := &udp.ListenConfig{}
	listener, err := llc.Listen(ctx, localhost)
	if err != nil {
		return nil, fmt.Errorf("listen UDP: %w", err)
	}

	return NewPeer(connector, listener, ops...), nil
}

// NewPeer creates a new Peer which connects to the host with connector and listents for new connection using listener.
func NewPeer(connector HostConnector, listener net.Listener, ops ...PeerOption) *Peer {
	pf := Peer{
		connector: connector,
		listener:  listener,
		logger:    slog.Default(),
		links:     make(map[*link]struct{}),
	}

	for _, o := range ops {
		o(&pf)
	}

	return &pf
}

// Forward accepts a connection on the listener and forwards it to the host identified by the key.
// Can be called multiple time to connect to different hosts.
// Use [Peer.Addr] to get the address on which the peer is listening.
func (p *Peer) Forward(ctx context.Context, key auth.Key, peerID string) error {
	hostStream, err := p.connector.ConnectToHost(ctx, key, peerID)
	if err != nil {
		return fmt.Errorf("connect game: %w", err)
	}
	p.logger.Debug("connected to the game", "key", key, "peer_id", peerID)

	gameConn, err := p.listener.Accept()
	if err != nil {
		return fmt.Errorf("accept game connection: %w", err)
	}
	p.logger.Debug("connection accepted", "local_addr", gameConn.LocalAddr(), "remote_addr", gameConn.RemoteAddr())

	l := newLink(gameConn, hostStream)
	p.trackLink(l, true)
	defer p.trackLink(l, false)

	return l.serve(ctx)
}

// Addr returns the address on which the peer is listening for incoming connections.
func (p *Peer) Addr() net.Addr {
	return p.listener.Addr()
}

// Close closes the listener and all active links.
func (p *Peer) Close() error {
	p.mux.Lock()
	defer p.mux.Unlock()

	var err error
	if closeErr := p.listener.Close(); closeErr != nil {
		err = fmt.Errorf("close listener: %w", closeErr)
	}

	for l := range p.links {
		if closeErr := l.close(); closeErr != nil {
			err = closeErr
		}
	}

	return err
}

func (p *Peer) trackLink(l *link, add bool) {
	p.mux.Lock()
	defer p.mux.Unlock()

	if add {
		p.links[l] = struct{}{}
	} else {
		delete(p.links, l)
	}
}

// PeerOption allows to configure the Peer.
type PeerOption func(*Peer)

// WithPeerLogger sets the logger for the Peer.
func WithPeerLogger(l *slog.Logger) PeerOption {
	return func(f *Peer) {
		f.logger = l
	}
}
