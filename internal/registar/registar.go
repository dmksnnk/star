package registar

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"

	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/relay"
	"github.com/quic-go/quic-go/http3"
)

var (
	ErrHostNotFound      = errors.New("host not found")
	ErrHostAlreadyExists = errors.New("host already exists")
)

type Registar2 struct {
	mux   sync.RWMutex
	hosts map[auth.Key]Peer

	relayAddr netip.AddrPort
	relay     *relay.UDPRelay
}

// TODO: implement host removal on host disconnect

var _ Registar = (*Registar2)(nil)

func NewRegistar2(relayAddr netip.AddrPort, relay *relay.UDPRelay) *Registar2 {
	return &Registar2{
		hosts:     make(map[auth.Key]Peer),
		relayAddr: relayAddr,
		relay:     relay,
	}
}

func (r *Registar2) Host(
	ctx context.Context,
	key auth.Key,
	streamer http3.HTTPStreamer,
	addrs AddrPair,
) error {
	r.mux.Lock()
	defer r.mux.Unlock()

	if _, ok := r.hosts[key]; ok {
		return ErrHostAlreadyExists
	}
	stream := streamer.HTTPStream()

	r.hosts[key] = NewPeer(addrs, stream)

	return nil
}

func (r *Registar2) Join(
	ctx context.Context,
	key auth.Key,
	streamer http3.HTTPStreamer,
	addrs AddrPair,
) error {
	host, ok := r.host(key)
	if !ok {
		return ErrHostNotFound
	}
	peer := NewPeer(addrs, streamer.HTTPStream())

	if err := initP2P(ctx, host, peer); err != nil {
		if !errors.Is(err, control.ErrConnectFailed) {
			return fmt.Errorf("init P2P connection: %w", err)
		}

		r.relay.AddRoute(host.addrs.Public, peer.addrs.Public)
		if err := initRelay(ctx, host, peer, r.relayAddr); err != nil {
			return fmt.Errorf("init relay connection: %w", err)
		}

		return nil
	}

	return nil
}

func (r *Registar2) host(key auth.Key) (Peer, bool) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	host, ok := r.hosts[key]
	return host, ok
}
