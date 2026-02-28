package registar_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/registartest"
	"go.uber.org/goleak"
	"golang.org/x/sync/errgroup"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var secret = []byte("secret")

var localhost = net.IPv4(127, 0, 0, 1)

func TestHoster(t *testing.T) {
	t.Run("registers and disconnects", func(t *testing.T) {
		srv := registartest.NewServer(t, secret)
		hostConnected := make(chan struct{})
		hostDisconnected := make(chan struct{})
		srv.Srv.HostConnected = func(key auth.Key, addrs registar.AddrPair) {
			close(hostConnected)
		}
		srv.Srv.HostDisconnected = func(key auth.Key, addrs registar.AddrPair) {
			close(hostDisconnected)
		}

		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		cc := registar.ClientConfig{
			TLSConfig:  srv.TLSConfig(),
			ListenAddr: &net.UDPAddr{IP: localhost, Port: 0},
		}
		hoster, err := cc.NewHoster(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("Host: %s", err)
		}

		wait(t, hostConnected, "host did not connect")

		if err := hoster.Close(); err != nil {
			t.Errorf("close hoster: %s", err)
		}

		wait(t, hostDisconnected, "host did not disconnect")
	})

	t.Run("accept cancelled", func(t *testing.T) {
		srv := registartest.NewServer(t, secret)

		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		cc := registar.ClientConfig{
			TLSConfig:  srv.TLSConfig(),
			ListenAddr: &net.UDPAddr{IP: localhost, Port: 0},
		}
		hoster, err := cc.NewHoster(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("Host: %s", err)
		}
		t.Cleanup(func() {
			if err := hoster.Close(); err != nil {
				t.Errorf("close hoster: %s", err)
			}
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err = hoster.Accept(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Accept: expected context.Canceled, got: %v", err)
		}
	})

	t.Run("register second time", func(t *testing.T) {
		srv := registartest.NewServer(t, secret)
		hostConnected := make(chan struct{})
		srv.Srv.HostConnected = func(key auth.Key, addrs registar.AddrPair) {
			close(hostConnected)
		}

		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		cc := registar.ClientConfig{
			TLSConfig:  srv.TLSConfig(),
			ListenAddr: &net.UDPAddr{IP: localhost, Port: 0},
		}
		hoster, err := cc.NewHoster(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("Host: %s", err)
		}
		t.Cleanup(func() {
			if err := hoster.Close(); err != nil {
				t.Errorf("close hoster: %s", err)
			}
		})

		wait(t, hostConnected, "host did not connect")

		_, err = cc.NewHoster(context.TODO(), srv.URL(), token)
		if !errors.Is(err, registar.ErrHostAlreadyRegistered) {
			t.Errorf("Host: expected host already registered error, got: %s", err)
		}
	})
}

func TestJoiner(t *testing.T) {
	t.Run("host not found", func(t *testing.T) {
		srv := registartest.NewServer(t, secret)

		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		cc := registar.ClientConfig{
			TLSConfig:  srv.TLSConfig(),
			ListenAddr: &net.UDPAddr{IP: localhost, Port: 0},
		}
		joiner, err := cc.NewJoiner(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("NewJoiner: %s", err)
		}

		_, err = joiner.Join(context.TODO())
		if !errors.Is(err, registar.ErrHostNotFound) {
			t.Errorf("Join: expected host not found error, got: %s", err)
		}

		if err := joiner.Close(); err != nil {
			t.Errorf("close joiner: %s", err)
		}
	})

	t.Run("join cancelled", func(t *testing.T) {
		srv := registartest.NewServer(t, secret)

		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		cc := registar.ClientConfig{
			TLSConfig:  srv.TLSConfig(),
			ListenAddr: &net.UDPAddr{IP: localhost, Port: 0},
		}
		joiner, err := cc.NewJoiner(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("NewJoiner: %s", err)
		}
		t.Cleanup(func() {
			if err := joiner.Close(); err != nil {
				t.Errorf("close joiner: %s", err)
			}
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err = joiner.Join(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Join: expected context.Canceled, got: %v", err)
		}
	})

	t.Run("connects to host", func(t *testing.T) {
		srv := registartest.NewServer(t, secret)
		hostConnected := make(chan struct{})
		hostDisconnected := make(chan struct{})
		peerConnected := make(chan struct{})
		srv.Srv.HostConnected = func(key auth.Key, addrs registar.AddrPair) {
			close(hostConnected)
		}
		srv.Srv.HostDisconnected = func(key auth.Key, addrs registar.AddrPair) {
			close(hostDisconnected)
		}
		srv.Srv.PeerConnected = func(key auth.Key, addrs registar.AddrPair) {
			close(peerConnected)
		}

		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		cc := registar.ClientConfig{
			TLSConfig:  srv.TLSConfig(),
			ListenAddr: &net.UDPAddr{IP: localhost, Port: 0},
		}
		hoster, err := cc.NewHoster(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("Host: %s", err)
		}

		wait(t, hostConnected, "host did not connect")

		var eg errgroup.Group
		hostConn := make(chan registar.DatagramConn)
		eg.Go(func() error {
			conn, err := hoster.Accept(context.TODO())
			if err != nil {
				return err
			}

			hostConn <- conn
			return nil
		})

		joiner, err := cc.NewJoiner(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("NewJoiner: %s", err)
		}

		peerConn, err := joiner.Join(context.TODO())
		if err != nil {
			t.Fatalf("Join: %s", err)
		}

		wait(t, peerConnected, "connection was not established")

		testDatagramConn(t, <-hostConn, peerConn)

		if err := joiner.Close(); err != nil {
			t.Errorf("close joiner: %s", err)
		}

		if err := hoster.Close(); err != nil {
			t.Errorf("close hoster: %s", err)
		}

		wait(t, hostDisconnected, "host did not disconnect")

		if err := eg.Wait(); err != nil {
			t.Errorf("wait for host accept: %s", err)
		}
	})

	t.Run("connects to host via relay", func(t *testing.T) {
		srv := registartest.NewServer(t, secret)
		hostConnected := make(chan struct{})
		hostDisconnected := make(chan struct{})
		peerConnected := make(chan struct{})
		srv.Srv.HostConnected = func(key auth.Key, addrs registar.AddrPair) {
			close(hostConnected)
		}
		srv.Srv.HostDisconnected = func(key auth.Key, addrs registar.AddrPair) {
			close(hostDisconnected)
		}
		srv.Srv.PeerConnected = func(key auth.Key, addrs registar.AddrPair) {
			close(peerConnected)
		}

		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		cc := registar.ClientConfig{
			TLSConfig:  srv.TLSConfig(),
			ListenAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
			DisableP2P: true,
		}
		hoster, err := cc.NewHoster(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("Host: %s", err)
		}

		wait(t, hostConnected, "host did not connect")

		var eg errgroup.Group
		hostConn := make(chan registar.DatagramConn)
		eg.Go(func() error {
			conn, err := hoster.Accept(context.TODO())
			if err != nil {
				return err
			}

			hostConn <- conn
			return nil
		})

		joiner, err := cc.NewJoiner(context.TODO(), srv.URL(), token)
		if err != nil {
			t.Fatalf("NewJoiner: %s", err)
		}

		peerConn, err := joiner.Join(context.TODO())
		if err != nil {
			t.Fatalf("Join: %s", err)
		}

		wait(t, peerConnected, "connection was not established")

		testDatagramConn(t, <-hostConn, peerConn)

		if err := joiner.Close(); err != nil {
			t.Errorf("close joiner: %s", err)
		}

		if err := hoster.Close(); err != nil {
			t.Errorf("close hoster: %s", err)
		}

		wait(t, hostDisconnected, "host did not disconnect")

		if err := eg.Wait(); err != nil {
			t.Errorf("wait for host accept: %s", err)
		}
	})
}

func testDatagramConn(t *testing.T, connA, connB registar.DatagramConn) {
	t.Helper()

	ctx := context.TODO()

	msgAB := []byte("hello from A to B")
	if err := connA.SendDatagram(msgAB); err != nil {
		t.Fatalf("connA.SendDatagram: %s", err)
	}
	got, err := connB.ReceiveDatagram(ctx)
	if err != nil {
		t.Fatalf("connB.ReceiveDatagram: %s", err)
	}
	if !bytes.Equal(got, msgAB) {
		t.Errorf("connB received %q, want %q", got, msgAB)
	}

	msgBA := []byte("hello from B to A")
	if err := connB.SendDatagram(msgBA); err != nil {
		t.Fatalf("connB.SendDatagram: %s", err)
	}
	got, err = connA.ReceiveDatagram(ctx)
	if err != nil {
		t.Fatalf("connA.ReceiveDatagram: %s", err)
	}
	if !bytes.Equal(got, msgBA) {
		t.Errorf("connA received %q, want %q", got, msgBA)
	}
}

func wait[T any](t *testing.T, ch <-chan T, msg string) T {
	t.Helper()

	var v T
	select {
	case v = <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout: %s", msg)
	}

	return v
}
