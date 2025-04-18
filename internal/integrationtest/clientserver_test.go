package integrationtest_test

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"net"
	"slices"
	"strconv"
	"testing"

	"github.com/dmksnnk/star/internal/host"
	"github.com/dmksnnk/star/internal/integrationtest"
	"github.com/dmksnnk/star/internal/peer"
	"github.com/dmksnnk/star/internal/platform"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/api/apitest"
	"github.com/dmksnnk/star/internal/registar/auth"
	"golang.org/x/sync/errgroup"
)

func TestSingleHostAndPeer(t *testing.T) {
	secret := []byte("secret")
	key := auth.NewKey()
	api := apitest.NewServer(t, secret, newRegistar(t))

	ctx := context.Background()
	var eg errgroup.Group

	// game host part
	gameHost := integrationtest.NewTestServer(t, func(req string) string {
		return req
	})
	hostForwarder := startHost(t, ctx, &eg, api.Client(t, secret), key, gameHost.Port())

	// game peer part
	peerForwarder := startPeer(t, ctx, &eg, api.Client(t, secret), key, "peer")
	gamePeer := integrationtest.NewTestClient(t, peerForwarder.LocalAddr().(*net.UDPAddr).Port)

	for i := 0; i < 10; i++ {
		msg := randSting(platform.MTU)
		resp, err := gamePeer.Call(msg)
		if err != nil {
			t.Fatalf("call: %s", err)
		}

		if want, got := msg, resp; want != got {
			t.Errorf("unexpected response, want: %d bytes, got: %d bytes", len(want), len(got))
		}
	}

	if err := hostForwarder.Close(); err != nil {
		t.Errorf("close host forwarder: %s", err)
	}

	if err := eg.Wait(); err != nil {
		t.Errorf("wait: %s", err)
	}
}

func TestOrder(t *testing.T) {
	secret := []byte("secret")
	key := auth.NewKey()
	api := apitest.NewServer(t, secret, newRegistar(t))

	var eg errgroup.Group
	ctx := context.Background()

	// host part

	hostListener := &platform.LocalListener{}
	hostConn, err := hostListener.ListenUDP(ctx, 0)
	if err != nil {
		t.Fatalf("listen local UDP: %s", err)
	}

	_ = startHost(t, ctx, &eg, api.Client(t, secret), key, hostConn.LocalAddr().(*net.UDPAddr).Port)

	// game peer part

	peerForwarder := startPeer(t, ctx, &eg, api.Client(t, secret), key, "peer")

	peerDialer := &platform.LocalDialer{}
	peerConn, err := peerDialer.DialUDP(context.TODO(), peerForwarder.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatalf("dial local UDP: %s", err)
	}

	for i := 1; i <= 4; i++ {
		if _, err := peerConn.Write([]byte(strconv.Itoa(i))); err != nil {
			t.Fatalf("write: %s", err)
		}
	}

	expected := []string{"1", "2", "3", "4"}
	var got []string
	buf := make([]byte, platform.MTU)
	for i := 1; i <= 4; i++ {
		n, _, err := hostConn.ReadFrom(buf)
		if err != nil {
			t.Fatalf("read from: %s", err)
		}

		got = append(got, string(buf[:n]))
	}

	if !slices.Equal(expected, got) {
		t.Errorf("unexpected received messages, want: %v, got: %v", expected, got)
	}
}

func TestMultipleClients(t *testing.T) {
	secret := []byte("secret")
	key := auth.NewKey()
	api := apitest.NewServer(t, secret, newRegistar(t))

	// game host part
	gameHost := integrationtest.NewTestServer(t, func(req string) string {
		return req
	})

	ctx := context.Background()
	var eg errgroup.Group

	hostForwarder := startHost(t, ctx, &eg, api.Client(t, secret), key, gameHost.Port())

	// game peer part
	for i := 0; i < 10; i++ {
		peerForwarder := startPeer(t, ctx, &eg, api.Client(t, secret), key, fmt.Sprintf("peer-%d", i))

		message := fmt.Sprintf("hello %d", i)
		gamePeer := integrationtest.NewTestClient(t, peerForwarder.LocalAddr().(*net.UDPAddr).Port)
		for i := 0; i < 10; i++ {
			resp, err := gamePeer.Call(message)
			if err != nil {
				t.Fatalf("call: %s", err)
			}

			if want, got := message, resp; want != got {
				t.Errorf("unexpected response, want: %q, got: %q", want, got)
			}
		}
	}

	if err := hostForwarder.Close(); err != nil {
		t.Errorf("wait host forwarder: %s", err)
	}

	if err := eg.Wait(); err != nil {
		t.Errorf("wait: %s", err)
	}
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

func startHost(t *testing.T, ctx context.Context, eg *errgroup.Group, client host.Client, key auth.Key, port int) *host.Forwarder {
	t.Helper()

	hostRegisterer := &host.Registerer{}
	hostForwarder, err := hostRegisterer.Register(context.Background(), client, key)
	if err != nil {
		t.Fatalf("register host: %s", err)
	}

	eg.Go(func() error {
		defer t.Log("host forwarder exited")
		return hostForwarder.ListenAndForward(ctx, port)
	})

	return hostForwarder
}

func startPeer(t *testing.T, ctx context.Context, eg *errgroup.Group, client peer.Client, key auth.Key, name string) *peer.Forwarder {
	t.Helper()

	peerConn := &peer.Connector{}
	peerForwarder, err := peerConn.ConnectAndListen(ctx, client, key, name)
	if err != nil {
		t.Fatalf("connect and listen: %s", err)
	}

	eg.Go(func() error {
		defer t.Log("peer forwarder exited")
		return peerForwarder.AcceptAndForward()
	})

	return peerForwarder
}

func randSting(n int) string {
	msg := make([]byte, n)
	crand.Read(msg)
	return string(msg)
}
