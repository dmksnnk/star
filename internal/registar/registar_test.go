package registar_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/platform/quictest"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var key = auth.NewKey()

func TestRegistar_Host_Join_ViaP2P(t *testing.T) {
	reg, _ := newRegistar(t)

	hostClientConn, hostServer := quictest.Pipe(t)

	hostAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}
	peerAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
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
	integrationtest.ServeAgent(t, hostAgent, hostServer)

	if err := reg.Host(context.TODO(), key, hostClientConn, hostAddrs); err != nil {
		t.Fatalf("Host: %s", err)
	}

	peerClient, peerServer := quictest.Pipe(t)

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
	integrationtest.ServeAgent(t, peerAgent, peerServer)

	if err := reg.Join(context.TODO(), key, peerClient, peerAddrs); err != nil {
		t.Fatalf("Join: %s", err)
	}

	wait(t, hostAgentCalled)
	wait(t, peerAgentCalled)

	// t.Run("remove connection on close", func(t *testing.T) {
	// 	err := reg.Host(context.TODO(), key, hostClientConn, hostAddrs)
	// 	if !errors.Is(err, registar.ErrHostAlreadyExists) {
	// 		t.Errorf("expected ErrHostAlreadyExists, got %s", err)
	// 	}

	// 	hostClientConn.CloseWithError(0, "bye!")

	// 	err = reg.Host(context.TODO(), key, hostClientConn, hostAddrs)
	// 	if err != nil {
	// 		t.Fatalf("unexpected error on re-Host after close: %s", err)
	// 	}
	// })
}

func TestRegistar_Host_Join_ViaRelay(t *testing.T) {
	reg, relayAddr := newRegistar(t)

	hostClient, hostServer := quictest.Pipe(t)

	hostAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}
	peerAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}

	hostAgent := control.NewAgent()
	hostAgentConnectRelayCalled := make(chan struct{})
	hostAgent.OnConnectTo(func(_ context.Context, cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress == peerAddrs.Private && cmd.PublicAddress == peerAddrs.Public {
			return false, nil // simulate P2P failure
		}

		if cmd.PrivateAddress == relayAddr && cmd.PublicAddress == relayAddr {
			close(hostAgentConnectRelayCalled)
			return true, nil // relay connection success
		}

		return true, nil
	})
	integrationtest.ServeAgent(t, hostAgent, hostServer)

	if err := reg.Host(context.TODO(), key, hostClient, hostAddrs); err != nil {
		t.Fatalf("Host: %s", err)
	}

	peerClient, peerServer := quictest.Pipe(t)

	peerAgent := control.NewAgent()
	peerAgentConnectRelayCalled := make(chan struct{})
	peerAgent.OnConnectTo(func(_ context.Context, cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress == hostAddrs.Private && cmd.PublicAddress == hostAddrs.Public {
			return false, nil // simulate P2P failure
		}

		if cmd.PrivateAddress == relayAddr && cmd.PublicAddress == relayAddr {
			close(peerAgentConnectRelayCalled)
			return true, nil // relay connection success
		}

		return true, nil
	})
	integrationtest.ServeAgent(t, peerAgent, peerServer)

	if err := reg.Join(context.TODO(), key, peerClient, peerAddrs); err != nil {
		t.Fatalf("Join: %s", err)
	}

	wait(t, hostAgentConnectRelayCalled)
	wait(t, peerAgentConnectRelayCalled)
}

func newRegistar(t *testing.T) (*registar.Registar2, netip.AddrPort) {
	relay, relayAddr := integrationtest.ServeRelay(t)

	rootCA, err := registar.NewRootCA()
	if err != nil {
		t.Fatalf("NewRootCA: %s", err)
	}
	authority := registar.NewAuthority(rootCA)
	reg := registar.NewRegistar2(authority, relayAddr, relay)

	return reg, relayAddr
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
