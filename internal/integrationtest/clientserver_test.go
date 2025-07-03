package integrationtest_test

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/forwarder"
	"github.com/dmksnnk/star/internal/integrationtest"
	"github.com/dmksnnk/star/internal/platform"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/api/apitest"
	"github.com/dmksnnk/star/internal/registar/auth"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestSingleHostAndPeer(t *testing.T) {
	secret := []byte("secret")
	key := auth.NewKey()
	api := apitest.NewServer(t, secret, newRegistar(t))

	ctx := context.Background()

	// game host part
	gameHost := integrationtest.NewTestUDPServer(t, func(req string) string {
		return req
	})
	startHost(t, ctx, api.Client(t, secret), key, gameHost.Port())

	// game peer part
	peerForwarder := startPeer(t, ctx, api.Client(t, secret), key, "peer")
	gamePeer := integrationtest.NewTestUDPClient(t, peerForwarder.Addr().(*net.UDPAddr).Port)

	for i := 0; i < 10; i++ {
		msg := randString(platform.MTU)
		resp, err := gamePeer.Call(msg)
		if err != nil {
			t.Fatalf("call: %s", err)
		}

		if want, got := msg, resp; want != got {
			t.Errorf("unexpected response, want: %d bytes, got: %d bytes", len(want), len(got))
		}
	}
}

func TestOrder(t *testing.T) {
	secret := []byte("secret")
	key := auth.NewKey()
	api := apitest.NewServer(t, secret, newRegistar(t))

	ctx := context.Background()

	// host part

	hostConn, err := net.ListenPacket("udp", "localhost:0")
	if err != nil {
		t.Fatalf("listen local UDP: %s", err)
	}

	startHost(t, ctx, api.Client(t, secret), key, hostConn.LocalAddr().(*net.UDPAddr).Port)

	// game peer part

	peerForwarder := startPeer(t, ctx, api.Client(t, secret), key, "peer")

	peerDialer := &platform.LocalDialer{}
	peerConn, err := peerDialer.DialUDP(context.TODO(), peerForwarder.Addr().(*net.UDPAddr).Port)
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

func TestMultipleRemoteClients(t *testing.T) {
	secret := []byte("secret")
	key := auth.NewKey()
	api := apitest.NewServer(t, secret, newRegistar(t))

	// game host part
	gameHost := integrationtest.NewTestUDPServer(t, func(req string) string {
		return req
	})

	ctx := context.Background()

	startHost(t, ctx, api.Client(t, secret), key, gameHost.Port())

	// game peer part
	for i := range 10 {
		peerForwarder := startPeer(t, ctx, api.Client(t, secret), key, fmt.Sprintf("peer-%d", i))
		gamePeer := integrationtest.NewTestUDPClient(t, peerForwarder.Addr().(*net.UDPAddr).Port)
		// set deadline to 5 seconds
		if err := gamePeer.Conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("set deadline: %s", err)
		}

		message := fmt.Sprintf("hello %d", i)
		for i := 0; i < 10; i++ {
			resp, err := gamePeer.Call(message)
			if err != nil {
				t.Fatalf("call peer %d: %s", i, err)
			}

			if want, got := message, resp; want != got {
				t.Errorf("peer %d, unexpected response, want: %q, got: %q", i, want, got)
			}
		}
	}
}

func TestMultipleLocalClients(t *testing.T) {
	secret := []byte("secret")
	key := auth.NewKey()
	api := apitest.NewServer(t, secret, newRegistar(t))

	// game host part
	gameHost := integrationtest.NewTestUDPServer(t, func(req string) string {
		return req
	})

	ctx := context.Background()

	startHost(t, ctx, api.Client(t, secret), key, gameHost.Port())

	// game peer part
	peer, err := forwarder.PeerListenLocalUDP(ctx, api.Client(t, secret))
	if err != nil {
		t.Fatalf("listen local UDP peer: %s", err)
	}

	var wg sync.WaitGroup
	port := peer.Addr().(*net.UDPAddr).Port
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := peer.Forward(ctx, key, fmt.Sprintf("peer-%d", i)); err != nil {
				t.Error("connect and forward:", err)
			}
		}(i)

		gamePeer := integrationtest.NewTestUDPClient(t, port)
		// set deadline to 5 seconds
		if err := gamePeer.Conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("set deadline: %s", err)
		}

		message := fmt.Sprintf("hello %d", i)
		for i := 0; i < 10; i++ {
			resp, err := gamePeer.Call(message)
			if err != nil {
				t.Fatalf("call peer %d: %s", i, err)
			}

			if want, got := message, resp; want != got {
				t.Errorf("peer %d, unexpected response, want: %q, got: %q", i, want, got)
			}
		}
	}

	if err := peer.Close(); err != nil {
		t.Errorf("close peer: %s", err)
	}
	wg.Wait()
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

func startHost(t *testing.T, ctx context.Context, client forwarder.PeerConnectionController, key auth.Key, port int) {
	t.Helper()

	listener, err := forwarder.RegisterHost(ctx, client, key)
	if err != nil {
		t.Fatalf("register game: %s", err)
	}
	host := forwarder.NewHost(
		forwarder.WithNotifyDisconnect(func(err error) {
			if err != nil {
				t.Errorf("host disconnected with error: %s", err)
			}
		}),
	)

	done := make(chan struct{})
	go func() error {
		defer close(done)
		defer t.Log("host forwarder exited")
		return host.Forward(ctx, listener, port)
	}()
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Errorf("close host: %s", err)
		}
		<-done
	})
}

func startPeer(t *testing.T, ctx context.Context, client forwarder.HostConnector, key auth.Key, name string) *forwarder.Peer {
	t.Helper()

	peer, err := forwarder.PeerListenLocalUDP(ctx, client)
	if err != nil {
		t.Fatalf("peer listen local UDP: %s", err)
	}

	done := make(chan struct{})
	go func() error {
		defer close(done)
		defer t.Log("peer forwarder exited")
		return peer.Forward(ctx, key, name)
	}()
	t.Cleanup(func() {
		if err := peer.Close(); err != nil {
			t.Errorf("close peer: %s", err)
		}
		<-done
	})

	return peer
}

func randString(n int) string {
	msg := make([]byte, n)
	crand.Read(msg)
	return string(msg)
}
