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
	relay       RelayRouter

	mux   sync.RWMutex
	hosts map[auth.Key]Peer

	ctx       context.Context
	ctxCancel context.CancelFunc
	joinCount sync.WaitGroup
}

type RelayRouter interface {
	AddRoute(a, b netip.AddrPort)
	RemoveRoute(a, b netip.AddrPort)
	RemoveAllRoutes(a netip.AddrPort)
}

type Peer interface {
	Addrs() AddrPair
	ConnectTo(ctx context.Context, public, private netip.AddrPort) error
	Context() context.Context
	Close(err error)
}

func NewRegistar2(ca *Authority, relayAddr netip.AddrPort, relay RelayRouter) *Registar2 {
	ctx, cancel := context.WithCancel(context.Background())

	return &Registar2{
		caAuthority: ca,
		relayAddr:   relayAddr,
		relay:       relay,
		hosts:       make(map[auth.Key]Peer),
		ctx:         ctx,
		ctxCancel:   cancel,
	}
}

func (r *Registar2) NewSessionCert(key auth.Key, csr *x509.CertificateRequest) (caCert, cert *x509.Certificate, err error) {
	return r.caAuthority.NewSessionCert(key, csr)
}

func (r *Registar2) Host(
	ctx context.Context,
	key auth.Key,
	peer Peer,
) error {
	r.mux.Lock()
	defer r.mux.Unlock()

	if _, ok := r.hosts[key]; ok {
		return ErrHostAlreadyExists
	}

	r.hosts[key] = peer
	context.AfterFunc(peer.Context(), func() {
		r.mux.Lock()
		defer r.mux.Unlock()

		delete(r.hosts, key)
		r.caAuthority.RemoveSessionCA(key)
		r.relay.RemoveAllRoutes(peer.Addrs().Public) // remove all routes associated with this host
	})

	return nil
}

func (r *Registar2) Join(
	ctx context.Context,
	key auth.Key,
	peer Peer,
) error {
	host, ok := r.host(key)
	if !ok {
		return ErrHostNotFound
	}

	r.joinCount.Add(1)
	go func() {
		defer r.joinCount.Done()

		if err := initP2P(r.ctx, host, peer); err != nil {
			if !errors.Is(err, control.ErrConnectFailed) {
				peer.Close(fmt.Errorf("init P2P connection: %w", err))
				return
			}

			r.relay.AddRoute(host.Addrs().Public, peer.Addrs().Public)
			if err := initRelay(r.ctx, host, peer, r.relayAddr); err != nil {
				peer.Close(fmt.Errorf("init relay connection: %w", err))
				return
			}

			// remove routes to/from peer on disconnect
			context.AfterFunc(peer.Context(), func() {
				r.relay.RemoveRoute(host.Addrs().Public, peer.Addrs().Public)
			})

			return
		}
	}()

	return nil
}

func (r *Registar2) host(key auth.Key) (Peer, bool) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	host, ok := r.hosts[key]
	return host, ok
}

func (r *Registar2) Close() error {
	r.ctxCancel()
	r.joinCount.Wait()

	r.mux.Lock()
	defer r.mux.Unlock()

	for key, host := range r.hosts {
		delete(r.hosts, key)
		r.caAuthority.RemoveSessionCA(key)
		r.relay.RemoveAllRoutes(host.Addrs().Public) // this will remove also all peers routes
	}

	return nil
}

func initP2P(ctx context.Context, host, peer Peer) error {
	ctx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	var hostErr, peerErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		hostErr = host.ConnectTo(ctx, peer.Addrs().Public, peer.Addrs().Private)
	}()
	go func() {
		defer wg.Done()
		peerErr = peer.ConnectTo(ctx, host.Addrs().Public, host.Addrs().Private)
	}()

	wg.Wait()

	// If either succeeded, P2P worked
	if hostErr == nil && peerErr == nil {
		return nil
	}

	// Both must fail with ErrConnectFailed for P2P to be considered failed
	if errors.Is(hostErr, control.ErrConnectFailed) && errors.Is(peerErr, control.ErrConnectFailed) {
		return control.ErrConnectFailed
	}

	return joinErrors(hostErr, peerErr)
}

func initRelay(ctx context.Context, host, peer Peer, relayAddr netip.AddrPort) error {
	ctx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	var hostErr, peerErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		hostErr = host.ConnectTo(ctx, relayAddr, relayAddr)
	}()
	go func() {
		defer wg.Done()
		peerErr = peer.ConnectTo(ctx, relayAddr, relayAddr)
	}()

	wg.Wait()

	return joinErrors(hostErr, peerErr)
}

func joinErrors(hostErr, peerErr error) error {
	var err error
	if hostErr != nil {
		err = fmt.Errorf("host failed to connect: %w", hostErr)
	}
	if peerErr != nil {
		err = errors.Join(err, fmt.Errorf("peer failed to connect: %w", peerErr))
	}

	return err
}
