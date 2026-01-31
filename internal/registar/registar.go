package registar

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/relay"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

var (
	ErrHostNotFound      = errors.New("host not found")
	ErrHostAlreadyExists = errors.New("host already exists")
)

const (
	defaultConnectTimeout = time.Minute
)

type AddrPair struct {
	Public  netip.AddrPort
	Private netip.AddrPort
}

type Registar2 struct {
	caAuthority *Authority
	relayAddr   netip.AddrPort
	relay       *relay.UDPRelay

	mux   sync.RWMutex
	hosts map[auth.Key]peer
}

// TODO: implement host removal on host disconnect
// TODO: implement removeval of session cert on host disconnect
// TODO: implement of removal of relay route on peer and host disconnect -> not need for TTLs in relay
// TODO: implement shutdown of registar, should close all connections

var _ Registar = (*Registar2)(nil)

func NewRegistar2(ca *Authority, relayAddr netip.AddrPort, relay *relay.UDPRelay) *Registar2 {
	return &Registar2{
		caAuthority: ca,
		relayAddr:   relayAddr,
		relay:       relay,
		hosts:       make(map[auth.Key]peer),
	}
}

func (r *Registar2) NewSessionCert(key auth.Key, csr *x509.CertificateRequest) (caCert, cert *x509.Certificate, err error) {
	return r.caAuthority.NewSessionCert(key, csr)
}

func (r *Registar2) Host(
	ctx context.Context,
	key auth.Key,
	conn *quic.Conn,
	addrs AddrPair,
) error {
	r.mux.Lock()
	defer r.mux.Unlock()

	if _, ok := r.hosts[key]; ok {
		return ErrHostAlreadyExists
	}

	r.hosts[key] = newPeer(addrs, conn)
	context.AfterFunc(conn.Context(), func() {
		r.mux.Lock()
		defer r.mux.Unlock()

		delete(r.hosts, key)
		r.caAuthority.RemoveSessionCA(key)
	})

	return nil
}

func (r *Registar2) Join(
	ctx context.Context,
	key auth.Key,
	conn *quic.Conn,
	addrs AddrPair,
) error {
	host, ok := r.host(key)
	if !ok {
		return ErrHostNotFound
	}
	peer := newPeer(addrs, conn)

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

func (r *Registar2) host(key auth.Key) (peer, bool) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	host, ok := r.hosts[key]
	return host, ok
}

type peer struct {
	addrs      AddrPair
	controller *control.Controller
}

func newPeer(addrs AddrPair, conn *quic.Conn) peer {
	return peer{
		addrs:      addrs,
		controller: control.NewController(conn),
	}
}

func initP2P(ctx context.Context, host, peer peer) error {
	ctx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return host.controller.ConnectTo(ctx, peer.addrs.Public, peer.addrs.Private)
	})
	eg.Go(func() error {
		return peer.controller.ConnectTo(ctx, host.addrs.Public, host.addrs.Private)
	})

	return eg.Wait()
}

func initRelay(ctx context.Context, host, peer peer, relayAddr netip.AddrPort) error {
	ctx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return host.controller.ConnectTo(ctx, relayAddr, relayAddr)
	})
	eg.Go(func() error {
		return peer.controller.ConnectTo(ctx, relayAddr, relayAddr)
	})

	return eg.Wait()
}
