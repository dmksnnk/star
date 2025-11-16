package registar_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestRegistar_Host_Join_ViaP2P(t *testing.T) {
	key := auth.NewKey()
	secret := []byte("secret")

	relay, relayAddr := integrationtest.ServeRelay(t)

	reg := registar.NewRegistar2(relayAddr, relay)

	srv := integrationtest.NewServer(t, secret, reg)

	host := integrationtest.NewClient(t, integrationtest.NewLocalQUICTransport(t), srv, secret, key)
	peer := integrationtest.NewClient(t, integrationtest.NewLocalQUICTransport(t), srv, secret, key)

	hostAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}
	peerAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}

	hostStream, err := host.Host(context.TODO(), hostAddrs)
	if err != nil {
		t.Fatalf("Host: %s", err)
	}

	hostAgent := control.NewAgent()
	hostAgentCalled := make(chan struct{})
	hostAgent.OnConnectTo(func(_ context.Context, cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress != peerAddrs.Private {
			t.Errorf("expected to receive peer's private address %q, got %q", peerAddrs.Private, cmd.PrivateAddress)
		}
		if cmd.PublicAddress != peerAddrs.Public {
			t.Errorf("expected to receive peer's public address %q, got %q", peerAddrs.Public, cmd.PublicAddress)
		}

		close(hostAgentCalled)
		return true, nil
	})
	integrationtest.ServeAgent(t, hostAgent, hostStream)

	peerStream, err := peer.Join(context.TODO(), peerAddrs)
	if err != nil {
		t.Fatalf("Join: %s", err)
	}

	peerAgent := control.NewAgent()
	peerAgentCalled := make(chan struct{})
	peerAgent.OnConnectTo(func(_ context.Context, cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress != hostAddrs.Private {
			t.Errorf("expected to receive host's private address %q, got %q", hostAddrs.Private, cmd.PrivateAddress)
		}

		if cmd.PublicAddress != hostAddrs.Public {
			t.Errorf("expected to receive host's public address %q, got %q", hostAddrs.Public, cmd.PublicAddress)
		}

		close(peerAgentCalled)
		return true, nil
	})
	integrationtest.ServeAgent(t, peerAgent, peerStream)

	wait(t, hostAgentCalled)
	wait(t, peerAgentCalled)
}

func TestRegistar_Host_Join_ViaRelay(t *testing.T) {
	key := auth.NewKey()
	secret := []byte("secret")

	relay, relayAddr := integrationtest.ServeRelay(t)

	reg := registar.NewRegistar2(relayAddr, relay)

	srv := integrationtest.NewServer(t, secret, reg)

	host := integrationtest.NewClient(t, integrationtest.NewLocalQUICTransport(t), srv, secret, key)
	peer := integrationtest.NewClient(t, integrationtest.NewLocalQUICTransport(t), srv, secret, key)

	hostAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}
	peerAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}

	hostStream, err := host.Host(context.TODO(), hostAddrs)
	if err != nil {
		t.Fatalf("Host: %s", err)
	}

	hostAgent := control.NewAgent()
	hostAgentConnectRelayCalled := make(chan struct{})
	hostAgent.OnConnectTo(func(_ context.Context, cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress == peerAddrs.Private && cmd.PublicAddress == peerAddrs.Public {
			return false, nil // simultate P2P failure
		}

		if cmd.PrivateAddress == relayAddr && cmd.PublicAddress == relayAddr {
			close(hostAgentConnectRelayCalled)
			return true, nil // relay connection success
		}

		return true, nil
	})
	integrationtest.ServeAgent(t, hostAgent, hostStream)

	peerStream, err := peer.Join(context.TODO(), peerAddrs)
	if err != nil {
		t.Fatalf("Join: %s", err)
	}

	peerAgent := control.NewAgent()
	peerAgentConnectRelayCalled := make(chan struct{})
	peerAgent.OnConnectTo(func(_ context.Context, cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress == hostAddrs.Private && cmd.PublicAddress == hostAddrs.Public {
			return false, nil // simultate P2P failure
		}

		if cmd.PrivateAddress == relayAddr && cmd.PublicAddress == relayAddr {
			close(peerAgentConnectRelayCalled)
			return true, nil // relay connection success
		}

		return true, nil
	})
	integrationtest.ServeAgent(t, peerAgent, peerStream)

	wait(t, hostAgentConnectRelayCalled)
	wait(t, peerAgentConnectRelayCalled)
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
