package registar_test

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var key = auth.NewKey()

func TestRegistar_Host_Join_ViaP2P(t *testing.T) {
	relayAddr := netip.MustParseAddrPort("10.0.0.1:9999")
	relayRouter := newFakeRelayRouter()
	reg, _ := newRegistar(t, relayAddr, relayRouter)

	hostAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}
	peerAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.2:1234"),
		Private: netip.MustParseAddrPort("10.0.0.2:4567"),
	}

	hostAgentConnectPeerCalled := make(chan struct{})
	hostCtx, hostCancel := context.WithCancel(context.Background())
	hostPeer := newFakePeer(hostAddrs, hostCtx, func(ctx context.Context, public, private netip.AddrPort) error {
		if public == peerAddrs.Public && private == peerAddrs.Private {
			close(hostAgentConnectPeerCalled)
			return nil // P2P connection success
		}

		return nil
	})

	if err := reg.Host(context.TODO(), key, hostPeer); err != nil {
		t.Fatalf("Host: %s", err)
	}

	peerAgentConnectPeerCalled := make(chan struct{})
	peerCtx, peerCancel := context.WithCancel(context.Background())
	peerPeer := newFakePeer(peerAddrs, peerCtx, func(ctx context.Context, public, private netip.AddrPort) error {
		if public == hostAddrs.Public && private == hostAddrs.Private {
			close(peerAgentConnectPeerCalled)
			return nil // P2P connection success
		}

		return nil
	})

	if err := reg.Join(context.TODO(), key, peerPeer); err != nil {
		t.Fatalf("Join: %s", err)
	}

	wait(t, hostAgentConnectPeerCalled)
	wait(t, peerAgentConnectPeerCalled)

	peerCancel()
	hostCancel()
}

func TestRegistar_Host_Join_ViaRelay(t *testing.T) {
	relayAddr := netip.MustParseAddrPort("10.0.0.1:9999")
	relayRouter := newFakeRelayRouter()
	reg, relayAddr := newRegistar(t, relayAddr, relayRouter)

	hostAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}
	peerAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}

	hostAgentConnectRelayCalled := make(chan struct{})
	hostCtx, hostCancel := context.WithCancel(context.Background())
	hostPeer := newFakePeer(hostAddrs, hostCtx, func(ctx context.Context, public, private netip.AddrPort) error {
		if public == peerAddrs.Public && private == peerAddrs.Private {
			return control.ErrConnectFailed // simulate P2P failure
		}

		if private == relayAddr && public == relayAddr {
			close(hostAgentConnectRelayCalled)
			return nil // relay connection success
		}

		return nil
	})

	if err := reg.Host(context.TODO(), key, hostPeer); err != nil {
		t.Fatalf("Host: %s", err)
	}

	peerAgentConnectRelayCalled := make(chan struct{})
	peerCtx, peerCancel := context.WithCancel(context.Background())
	peerPeer := newFakePeer(peerAddrs, peerCtx, func(ctx context.Context, public, private netip.AddrPort) error {
		if public == hostAddrs.Public && private == hostAddrs.Private {
			return control.ErrConnectFailed // simulate P2P failure
		}

		if private == relayAddr && public == relayAddr {
			close(peerAgentConnectRelayCalled)
			return nil // relay connection success
		}

		return nil
	})

	if err := reg.Join(context.TODO(), key, peerPeer); err != nil {
		t.Fatalf("Join: %s", err)
	}

	wait(t, hostAgentConnectRelayCalled)
	wait(t, peerAgentConnectRelayCalled)

	if dst, ok := relayRouter.Routes(hostAddrs.Public); !ok || dst != peerAddrs.Public {
		t.Errorf("expected relay route from host to peer to be established")
	}

	if dst, ok := relayRouter.Routes(peerAddrs.Public); !ok || dst != hostAddrs.Public {
		t.Errorf("expected relay route from peer to host to be established")
	}

	peerCancel()
	// if _, ok := relayRouter.Routes(peerAddrs.Public); ok {
	// 	t.Errorf("expected relay route from host to peer to be removed on peer disconnect")
	// }
	hostCancel()
	// if _, ok := relayRouter.Routes(hostAddrs.Public); ok {
	// 	t.Errorf("expected relay route from peer to host to be removed on host disconnect")
	// }
}

func newRegistar(t *testing.T, relayAddr netip.AddrPort, relay registar.RelayRouter) (*registar.Registar2, netip.AddrPort) {
	rootCA, err := registar.NewRootCA()
	if err != nil {
		t.Fatalf("NewRootCA: %s", err)
	}
	authority := registar.NewAuthority(rootCA)
	reg := registar.NewRegistar2(authority, relayAddr, relay)

	t.Cleanup(func() {
		if err := reg.Close(); err != nil {
			t.Errorf("close registar: %s", err)
		}
	})

	return reg, relayAddr
}

type fakeRelayRouter struct {
	mu     sync.Mutex
	routes map[netip.AddrPort]netip.AddrPort
}

func newFakeRelayRouter() *fakeRelayRouter {
	return &fakeRelayRouter{
		routes: make(map[netip.AddrPort]netip.AddrPort),
	}
}

func (r *fakeRelayRouter) AddRoute(a, b netip.AddrPort) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.routes[a] = b
	r.routes[b] = a
}

func (r *fakeRelayRouter) RemoveRoute(a, b netip.AddrPort) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.routes, a)
	delete(r.routes, b)
}

func (r *fakeRelayRouter) RemoveAllRoutes(a netip.AddrPort) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.routes, a)
	for _, dst := range r.routes {
		if dst == a {
			delete(r.routes, dst)
		}
	}
}

func (r *fakeRelayRouter) Routes(a netip.AddrPort) (netip.AddrPort, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	dst, ok := r.routes[a]
	return dst, ok
}

type fakePeer struct {
	addrs             registar.AddrPair
	context           context.Context
	connectToCallback func(ctx context.Context, public, private netip.AddrPort) error
	closedWith        error
}

func newFakePeer(addrs registar.AddrPair, ctx context.Context, connectToCallback func(ctx context.Context, public, private netip.AddrPort) error) *fakePeer {
	return &fakePeer{
		addrs:             addrs,
		context:           ctx,
		connectToCallback: connectToCallback,
	}
}

func (p *fakePeer) Addrs() registar.AddrPair {
	return p.addrs
}

func (p *fakePeer) ConnectTo(ctx context.Context, public, private netip.AddrPort) error {
	return p.connectToCallback(ctx, public, private)
}

func (p *fakePeer) Context() context.Context {
	return p.context
}

func (p *fakePeer) Close(err error) {
	p.closedWith = err
}

func wait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	var v T
	select {
	case v = <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	return v
}
