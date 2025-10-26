package registar_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"go.uber.org/goleak"
	"golang.org/x/sync/errgroup"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestRegistar_notifiesOnSessionDone(t *testing.T) {
	key := auth.NewKey()

	reg := registar.NewRegistar2()
	sessionDone := make(chan struct{})
	reg.OnSessionDone(func(k auth.Key, err error) {
		defer close(sessionDone)

		if k != key {
			t.Errorf("expected key %q, got %q", key, k)
		}

		if !errcode.IsRemoteStreamError(err, errcode.HostClosed) {
			t.Errorf("expected a remote stream error with code HostClosed, got %v", err)
		}
	})

	secret := []byte("secret")
	srv := integrationtest.NewServer(t, secret, reg)

	host := integrationtest.NewClient(t, integrationtest.NewLocalQUICTransport(t), srv, secret, key)

	hostAddrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("10.0.0.1:1234"),
		Private: netip.MustParseAddrPort("10.0.0.1:4567"),
	}
	stream, err := host.Host(context.TODO(), hostAddrs)
	if err != nil {
		t.Fatalf("Host: %s", err)
	}

	stream.CancelRead(errcode.HostClosed)

	wait(t, sessionDone)

	_, ok := reg.Session(key)
	if ok {
		t.Errorf("expected session to be removed after done notification")
	}
}

func TestRegistar_JoinSession(t *testing.T) {
	key := auth.NewKey()
	var agentsWg errgroup.Group

	reg := registar.NewRegistar2()
	sessionDone := make(chan struct{})
	reg.OnSessionDone(func(k auth.Key, err error) {
		defer close(sessionDone)

		if k != key {
			t.Errorf("expected key %q, got %q", key, k)
		}

		if !errcode.IsRemoteStreamError(err, errcode.Cancelled) {
			t.Errorf("expected a remote stream error with code Cancelled, got %v", err)
		}
	})

	secret := []byte("secret")
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

	joined := make(chan struct{})
	reg.OnJoined(func(k auth.Key, addrs registar.AddrPair) {
		if k != key {
			t.Errorf("expected key %q, got %q", key, k)
		}

		if peerAddrs.Private != addrs.Private {
			t.Errorf("expected private address %q, got %q", peerAddrs.Private, addrs.Private)
		}
		if peerAddrs.Public != addrs.Public {
			t.Errorf("expected public address %q, got %q", peerAddrs.Public, addrs.Public)
		}

		close(joined)
	})

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
	agentsWg.Go(func() error {
		return hostAgent.Serve(hostStream)
	})

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
	agentsWg.Go(func() error {
		return peerAgent.Serve(peerStream)
	})

	wait(t, hostAgentCalled)
	wait(t, peerAgentCalled)
	wait(t, joined)

	_, ok := reg.Session(key)
	if !ok {
		t.Error("expected session to exist")
	}

	peerStream.CancelRead(errcode.Cancelled)
	peerStream.CancelWrite(errcode.Cancelled)
	hostStream.CancelRead(errcode.Cancelled)
	hostStream.CancelWrite(errcode.Cancelled)

	if err := agentsWg.Wait(); err != nil {
		if !errcode.IsLocalHTTPError(err, errcode.Cancelled) {
			t.Errorf("expected a local HTTP3 error with code Cancelled, got %v", err)
		}
	}

	wait(t, sessionDone)

	_, ok = reg.Session(key)
	if ok {
		t.Errorf("expected session to be removed after done notification")
	}
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
