package integrationtest_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"github.com/dmksnnk/star/internal/registar/p2p"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

var (
	secret = []byte("secret")
	key    = auth.NewKey()
)

func TestDiscovery(t *testing.T) {
	server := integrationtest.RunDiscovery(t)
	clientConn := integrationtest.NewLocalUDPConn(t)
	addrs := integrationtest.Discover(t, clientConn, server)

	if clientConn.LocalAddr().(*net.UDPAddr).AddrPort().Compare(addrs.Public) != 0 {
		t.Fatalf("expected %v, got: %v", clientConn.LocalAddr(), addrs.Public)
	}
}

func TestConnectTo(t *testing.T) {
	reg := registar.NewRegistar2()
	srv := integrationtest.NewServer(t, secret, reg)

	serverAddr := integrationtest.RunDiscovery(t)

	hostConn := integrationtest.NewLocalUDPConn(t)
	hostAddrs := integrationtest.Discover(t, hostConn, serverAddr)
	hostTransport := &quic.Transport{
		Conn: hostConn,
	}
	host := integrationtest.NewClient(t, hostTransport, srv, secret, key)

	hostStream, err := host.Host(context.TODO(), hostAddrs)
	if err != nil {
		t.Fatalf("Host: %s", err)
	}

	var eg errgroup.Group

	hostConnector := p2p.NewConnector(hostTransport, host.TLSConfig())
	hostAgent := control.NewAgent()
	hostAgentConnected := make(chan struct{})
	hostAgent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		defer close(hostAgentConnected)

		fmt.Printf("connecting to peer at %s / %s\n", cmd.PublicAddress, cmd.PrivateAddress)
		conn, err := hostConnector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			return false, fmt.Errorf("connect to peer: %w", err)
		}
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			return false, fmt.Errorf("open stream: %w", err)
		}

		_, err = stream.Write([]byte("hello from host"))
		if err != nil {
			return false, fmt.Errorf("write to stream: %w", err)
		}

		buf := make([]byte, 1024)
		n, err := stream.Read(buf)
		if err != nil {
			return false, fmt.Errorf("read from stream: %w", err)
		}

		expected := "hello from peer"
		received := string(buf[:n])
		if received != expected {
			return false, fmt.Errorf("expected %q, got %q", expected, received)
		}

		return true, nil
	})
	eg.Go(func() error {
		return hostAgent.Serve(hostStream)
	})

	peerConn := integrationtest.NewLocalUDPConn(t)
	peerAddrs := integrationtest.Discover(t, peerConn, serverAddr)
	peerTransport := &quic.Transport{
		Conn: peerConn,
	}
	peer := integrationtest.NewClient(t, peerTransport, srv, secret, key)

	peerStream, err := peer.Join(context.TODO(), peerAddrs)
	if err != nil {
		t.Fatalf("Join: %s", err)
	}

	peerConnector := p2p.NewConnector(peerTransport, peer.TLSConfig())
	peerAgent := control.NewAgent()
	peerAgentConnected := make(chan struct{})
	peerAgent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		defer close(peerAgentConnected)

		fmt.Printf("connecting to host at %s / %s\n", cmd.PublicAddress, cmd.PrivateAddress)
		conn, err := peerConnector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			return false, fmt.Errorf("connect to host: %w", err)
		}
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			t.Errorf("accept stream: %s", err)
			return true, nil
		}

		buf := make([]byte, 1024)
		n, err := stream.Read(buf)
		if err != nil {
			return false, fmt.Errorf("read from stream: %w", err)
		}

		expected := "hello from host"
		received := string(buf[:n])
		if received != expected {
			return false, fmt.Errorf("expected %q, got %q", expected, received)
		}

		_, err = stream.Write([]byte("hello from peer"))
		if err != nil {
			t.Errorf("write to stream: %s", err)
			return true, nil
		}

		return true, nil
	})
	eg.Go(func() error {
		return peerAgent.Serve(peerStream)
	})

	wait(t, peerAgentConnected)
	wait(t, hostAgentConnected)

	peerStream.CancelRead(errcode.Cancelled)
	peerStream.CancelWrite(errcode.Cancelled)
	hostStream.CancelRead(errcode.Cancelled)
	hostStream.CancelWrite(errcode.Cancelled)

	if err := eg.Wait(); err != nil {
		if !errcode.IsLocalHTTPError(err, errcode.Cancelled) {
			t.Errorf("expected a local HTTP3 error with code Cancelled, got %v", err)
		}
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
