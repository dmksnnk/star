package api_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/api"
	"github.com/dmksnnk/star/internal/registar/api/apitest"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
	"go.uber.org/goleak"
	"golang.org/x/sync/errgroup"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var clientCloseErr = &quic.ApplicationError{
	ErrorCode:    quic.ApplicationErrorCode(0x0),
	Remote:       true,
	ErrorMessage: "client closed",
}

func TestClient(t *testing.T) {
	secret := []byte("secret")

	t.Run("register game", func(t *testing.T) {
		svc := newRegistar(t)
		srv := apitest.NewServer(t, secret, svc)
		key := auth.NewKey()

		hostDisconnected := notifyHostDisconnected(t, svc, key, clientCloseErr)

		client := srv.Client(t, secret)
		_, err := client.RegisterGame(context.Background(), key)
		if err != nil {
			t.Fatalf("register game: %s", err)
		}

		if err := client.Close(); err != nil {
			t.Fatalf("client close: %s", err)
		}

		waitNotify(t, hostDisconnected, "host disconnected")
	})

	t.Run("connect game", func(t *testing.T) {
		svc := newRegistar(t)
		srv := apitest.NewServer(t, secret, svc)
		cl := srv.Client(t, secret)
		key := auth.NewKey()

		done := make(chan struct{})
		<-runHost(t, cl, key, done)

		peerConnected := notifyPeerConnected(t, svc, key, "peer")
		_, err := cl.ConnectGame(context.Background(), key, "peer")
		if err != nil {
			t.Fatalf("connect game: %s", err)
		}

		waitNotify(t, peerConnected, "peer connected")
		close(done)
	})

	t.Run("connect to non-existing game", func(t *testing.T) {
		srv := apitest.NewServer(t, secret, newRegistar(t))
		cl := srv.Client(t, secret)
		key := auth.NewKey()
		_, err := cl.ConnectGame(context.Background(), key, "peer")
		if !httpplatform.IsNotFound(err) {
			t.Errorf("expected NotFound error, got: %s", err)
		}
	})

	t.Run("invalid secret", func(t *testing.T) {
		srv := apitest.NewServer(t, secret, newRegistar(t))
		cl := srv.Client(t, []byte("invalid secret"))
		_, err := cl.RegisterGame(context.Background(), auth.NewKey())
		if !httpplatform.IsUnauthorized(err) {
			t.Errorf("expected Unauthorized error, got: %s", err)
		}

		_, err = cl.ConnectGame(context.Background(), auth.NewKey(), "peer")
		if !httpplatform.IsUnauthorized(err) {
			t.Errorf("expected Unauthorized error, got: %s", err)
		}

		_, err = cl.Forward(context.Background(), auth.NewKey(), "peer")
		if !httpplatform.IsUnauthorized(err) {
			t.Errorf("expected Unauthorized error, got: %s", err)
		}
	})

	t.Run("peer closes client", func(t *testing.T) {
		key := auth.NewKey()
		svc := newRegistar(t)
		srv := apitest.NewServer(t, secret, svc)

		hostDisconnected := notifyHostDisconnected(t, svc, key, clientCloseErr)

		peerDone := make(chan struct{})
		<-runHost(t, srv.Client(t, secret), key, peerDone)

		peerConnected := notifyPeerConnected(t, svc, key, "peer")
		peerDisconnected := notifyPeerDisconnected(t, svc, key, "peer", clientCloseErr)
		peer := srv.Client(t, secret)
		_, err := peer.ConnectGame(context.Background(), key, "peer")
		if err != nil {
			t.Fatalf("connect game: %s", err)
		}

		// wait for peer to be accepted
		waitNotify(t, peerConnected, "peer connected")
		if want, got := 1, len(svc.Peers(key)); want != got {
			t.Errorf("unexpected number of peers, want: %d, got: %d", want, got)
		}

		if err := peer.Close(); err != nil {
			t.Fatalf("peer client: %s", err)
		}

		<-peerDisconnected
		if want, got := 0, len(svc.Peers(key)); want != got {
			t.Errorf("unexpected number of peers, want: %d, got: %d", want, got)
		}

		close(peerDone)

		waitNotify(t, hostDisconnected, "host disconnected")
	})

	t.Run("peer closes stream", func(t *testing.T) {
		svc := newRegistar(t)
		srv := apitest.NewServer(t, secret, svc)
		key := auth.NewKey()

		hostDisconnected := notifyHostDisconnected(t, svc, key, clientCloseErr)

		peerDone := make(chan struct{})
		<-runHost(t, srv.Client(t, secret), key, peerDone)

		peerConnected := notifyPeerConnected(t, svc, key, "peer")
		peerDisconnected := notifyPeerDisconnected(t, svc, key, "peer", nil)
		peer := srv.Client(t, secret)
		peerStream, err := peer.ConnectGame(context.Background(), key, "peer")
		if err != nil {
			t.Fatalf("connect game: %s", err)
		}

		// wait for peer to be accepted
		waitNotify(t, peerConnected, "peer connected")
		if want, got := 1, len(svc.Peers(key)); want != got {
			t.Errorf("unexpected number of peers, want: %d, got: %d", want, got)
		}

		peerStream.CancelWrite(errcode.Cancelled)
		peerStream.CancelRead(errcode.Cancelled)

		waitNotify(t, peerDisconnected, "peer disconnected")
		if want, got := 0, len(svc.Peers(key)); want != got {
			t.Errorf("unexpected number of peers, want: %d, got: %d", want, got)
		}

		close(peerDone)

		waitNotify(t, hostDisconnected, "host disconnected")
	})
}

func runHost(t *testing.T, client *api.Client, key auth.Key, done <-chan struct{}) <-chan struct{} {
	registered := make(chan struct{})
	var eg errgroup.Group
	eg.Go(func() error {
		controlStream, err := client.RegisterGame(context.Background(), key)
		if err != nil {
			return fmt.Errorf("register game: %s", err)
		}

		close(registered)

		l := control.Listen(controlStream, client, key)
		peerStream, err := l.AcceptForward()
		if err != nil {
			return fmt.Errorf("accept forward: %s", err)
		}

		<-done

		if err := peerStream.Close(); err != nil {
			return fmt.Errorf("close peer conn: %s", err)
		}

		if err := controlStream.Close(); err != nil {
			return fmt.Errorf("close control stream: %s", err)
		}

		return client.Close()
	})

	t.Cleanup(func() {
		if err := eg.Wait(); err != nil {
			t.Errorf("wait: %s", err)
		}
	})

	return registered
}

func newRegistar(t *testing.T) *registar.Registar {
	t.Helper()

	svc := registar.New()
	t.Cleanup(func() {
		if err := svc.Stop(); err != nil {
			t.Error("stop service:", err)
		}
	})

	return svc
}

func notifyPeerConnected(t *testing.T, svc *registar.Registar, key auth.Key, peerID string) <-chan struct{} {
	peerConnected := make(chan struct{})
	svc.NotifyPeerConnected(func(gotKey auth.Key, gotPeerID string) {
		defer close(peerConnected)

		if want, got := key, gotKey; want != got {
			t.Errorf("unexpected key, want: %s, got: %s", want, got)
		}

		if want, got := peerID, gotPeerID; want != got {
			t.Errorf("unexpected peer_id, want: %s, got: %s", want, got)
		}
	})

	return peerConnected
}

func notifyPeerDisconnected(t *testing.T, svc *registar.Registar, key auth.Key, peerID string, reason error) <-chan struct{} {
	peerDisconnected := make(chan struct{})
	svc.NotifyPeerDisconnected(func(gotKey auth.Key, gotPeerID string, gotReason error) {
		defer close(peerDisconnected)

		if want, got := key, gotKey; want != got {
			t.Errorf("unexpected key, want: %s, got: %s", want, got)
		}

		if want, got := peerID, gotPeerID; want != got {
			t.Errorf("unexpected peer_id, want: %s, got: %s", want, got)
		}

		if want, got := reason, gotReason; !errors.Is(got, want) {
			t.Errorf("unexpected reason, want: %s, got: %s", want, got)
		}
	})

	return peerDisconnected
}

func notifyHostDisconnected(t *testing.T, svc *registar.Registar, key auth.Key, reason error) <-chan struct{} {
	hostDisconnected := make(chan struct{})
	svc.NotifyHostDisconnected(func(gotKey auth.Key, gotReason error) {
		defer close(hostDisconnected)

		if want, got := key, gotKey; want != got {
			t.Errorf("unexpected key, want: %s, got: %s", want, got)
		}

		if want, got := reason, gotReason; !errors.Is(got, want) {
			t.Errorf("unexpected reason, want: %s, got: %s", want, got)
		}
	})

	return hostDisconnected
}

func waitNotify(t *testing.T, ch <-chan struct{}, reason string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Errorf("timeout waiting for notification: %s", reason)
	}
}
