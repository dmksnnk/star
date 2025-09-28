package registar_test

import (
	"context"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
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
	srv := newServer(t, secret, reg)

	host, err := registar.RegisterClient(context.TODO(), srv.TLSConfig(), srv.URL(), secret, key)
	if err != nil {
		t.Fatalf("register client: %s", err)
	}

	stream, err := host.Host(context.TODO())
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
	srv := newServer(t, secret, reg)

	host, err := registar.RegisterClient(context.TODO(), srv.TLSConfig(), srv.URL(), secret, key)
	if err != nil {
		t.Fatalf("register host: %s", err)
	}

	peer, err := registar.RegisterClient(context.TODO(), srv.TLSConfig(), srv.URL(), secret, key)
	if err != nil {
		t.Fatalf("register peer: %s", err)
	}

	joined := make(chan struct{})
	reg.OnJoined(func(k auth.Key, addrs registar.AddrPair) {
		if k != key {
			t.Errorf("expected key %q, got %q", key, k)
		}

		if peer.LocalAddr() != addrs.Private {
			t.Errorf("expected private address %q, got %q", peer.LocalAddr(), addrs.Private)
		}

		close(joined)
	})

	hostStream, err := host.Host(context.TODO())
	if err != nil {
		t.Fatalf("Host: %s", err)
	}

	hostAgent := control.NewAgent()
	hostAgentCalled := make(chan struct{})
	hostAgent.OnConnectTo(func(cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress != peer.LocalAddr() {
			t.Errorf("expected to receive peer's private address %q, got %q", peer.LocalAddr(), cmd.PrivateAddress)
		}
		close(hostAgentCalled)
		return true, nil
	})
	agentsWg.Go(func() error {
		return hostAgent.Serve(hostStream)
	})

	peerStream, err := peer.Join(context.TODO())
	if err != nil {
		t.Fatalf("Join: %s", err)
	}

	peerAgent := control.NewAgent()
	peerAgentCalled := make(chan struct{})
	peerAgent.OnConnectTo(func(cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress != host.LocalAddr() {
			t.Errorf("expected to receive host's private address %q, got %q", host.LocalAddr(), cmd.PrivateAddress)
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
