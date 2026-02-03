package integrationtest_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"github.com/dmksnnk/star/internal/registar/p2p"
	"github.com/quic-go/quic-go"
	"go.uber.org/goleak"
	"golang.org/x/sync/errgroup"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var (
	secret = []byte("secret")
	key    = auth.NewKey()
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
)

func Test_ConnectP2P(t *testing.T) {
	reg, _ := newRegistar(t)
	srv := integrationtest.NewServer(t, reg, secret)

	token := auth.NewToken(key, secret)
	cc := registar.ClientConfig{
		TLSConfig: srv.TLSConfig(),
	}

	hostTransport := integrationtest.NewLocalQUICTransport(t)
	hostClientConn, hostTLSConf, err := cc.Host(context.TODO(), hostTransport, srv.URL(), token)
	if err != nil {
		t.Fatalf("Host: %s", err)
	}

	hostConnector := p2p.NewConnector(hostTransport, hostTLSConf, p2p.WithLogger(logger.With("connector", "host")))
	hostAgent := control.NewAgent()
	hostAgentConnected := make(chan struct{})
	var hostP2PConn *quic.Conn
	hostAgent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		t.Logf("host: connecting to peer at %s / %s\n", cmd.PublicAddress, cmd.PrivateAddress)
		conn, err := hostConnector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			return false, fmt.Errorf("connect to peer: %w", err)
		}

		hostP2PConn = conn
		close(hostAgentConnected)

		return true, nil
	})
	integrationtest.ServeAgent(t, hostAgent, hostClientConn)

	// Setup peer - must be done before joining
	peerTransport := integrationtest.NewLocalQUICTransport(t)
	peerClientConn, peerTLSConf, err := cc.Join(context.TODO(), peerTransport, srv.URL(), token)
	if err != nil {
		t.Fatalf("Join: %s", err)
	}

	peerConnector := p2p.NewConnector(peerTransport, peerTLSConf, p2p.WithLogger(logger.With("connector", "peer")))
	peerAgent := control.NewAgent()
	peerAgentConnected := make(chan struct{})
	var peerP2PConn *quic.Conn
	peerAgent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		t.Logf("peer: connecting to host at %s / %s\n", cmd.PublicAddress, cmd.PrivateAddress)
		conn, err := peerConnector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			return false, fmt.Errorf("connect to host: %w", err)
		}

		peerP2PConn = conn
		close(peerAgentConnected)

		return true, nil
	})
	integrationtest.ServeAgent(t, peerAgent, peerClientConn)

	wait(t, hostAgentConnected)
	wait(t, peerAgentConnected)

	testPipeConn(t, hostP2PConn, peerP2PConn)
}

func Test_ConnectRelay(t *testing.T) {
	reg, relayAddr := newRegistar(t)
	srv := integrationtest.NewServer(t, reg, secret)

	token := auth.NewToken(key, secret)
	cc := registar.ClientConfig{
		TLSConfig: srv.TLSConfig(),
	}

	hostTransport := integrationtest.NewLocalQUICTransport(t)
	hostClientConn, hostTLSConf, err := cc.Host(context.TODO(), hostTransport, srv.URL(), token)
	if err != nil {
		t.Fatalf("Host: %s", err)
	}

	hostConnector := p2p.NewConnector(hostTransport, hostTLSConf, p2p.WithLogger(logger.With("connector", "host")))
	hostAgent := control.NewAgent()
	hostAgentConnected := make(chan struct{})
	var hostP2PConn *quic.Conn
	hostAgent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress != relayAddr && cmd.PublicAddress != relayAddr {
			return false, nil // simulate P2P failure
		}

		t.Logf("host: connecting to peer at %s / %s\n", cmd.PublicAddress, cmd.PrivateAddress)
		conn, err := hostConnector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			return false, fmt.Errorf("connect to peer: %w", err)
		}

		hostP2PConn = conn
		close(hostAgentConnected)

		return true, nil
	})
	integrationtest.ServeAgent(t, hostAgent, hostClientConn)

	// Setup peer - must be done before joining
	peerTransport := integrationtest.NewLocalQUICTransport(t)
	peerClientConn, peerTLSConf, err := cc.Join(context.TODO(), peerTransport, srv.URL(), token)
	if err != nil {
		t.Fatalf("Join: %s", err)
	}

	peerConnector := p2p.NewConnector(peerTransport, peerTLSConf, p2p.WithLogger(logger.With("connector", "peer")))
	peerAgent := control.NewAgent()
	peerAgentConnected := make(chan struct{})
	var peerP2PConn *quic.Conn
	peerAgent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		if cmd.PrivateAddress != relayAddr && cmd.PublicAddress != relayAddr {
			return false, nil // simulate P2P failure
		}

		t.Logf("peer: connecting to host at %s / %s\n", cmd.PublicAddress, cmd.PrivateAddress)
		conn, err := peerConnector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			return false, fmt.Errorf("connect to host: %w", err)
		}

		peerP2PConn = conn
		close(peerAgentConnected)

		return true, nil
	})
	integrationtest.ServeAgent(t, peerAgent, peerClientConn)

	wait(t, hostAgentConnected)
	wait(t, peerAgentConnected)

	testPipeConn(t, hostP2PConn, peerP2PConn)
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

// testPipeConn tests bidirectional communication between two QUIC connections.
// It opens a stream from connA and accepts it on connB, then tests data exchange.
func testPipeConn(t *testing.T, connA, connB *quic.Conn) {
	t.Helper()

	ctx := context.Background()

	msgA := []byte("from A to B")
	msgB := []byte("from B to A")

	var eg errgroup.Group

	// connA opens a stream, sends msgA, reads msgB
	eg.Go(func() error {
		streamA, err := connA.OpenStreamSync(ctx)
		if err != nil {
			return fmt.Errorf("connA open stream: %w", err)
		}

		if _, err := streamA.Write(msgA); err != nil {
			return fmt.Errorf("A write: %w", err)
		}

		buf := make([]byte, len(msgB))
		if _, err := io.ReadFull(streamA, buf); err != nil {
			return fmt.Errorf("A read: %w", err)
		}

		if string(buf) != string(msgB) {
			t.Errorf("A expected %q, got %q", msgB, buf)
		}

		return nil
	})

	// connB accepts the stream, reads msgA, sends msgB
	eg.Go(func() error {
		streamB, err := connB.AcceptStream(ctx)
		if err != nil {
			return fmt.Errorf("connB accept stream: %w", err)
		}

		buf := make([]byte, len(msgA))
		if _, err := io.ReadFull(streamB, buf); err != nil {
			return fmt.Errorf("B read: %w", err)
		}

		if string(buf) != string(msgA) {
			t.Errorf("B expected %q, got %q", msgA, buf)
		}

		if _, err := streamB.Write(msgB); err != nil {
			return fmt.Errorf("B write: %w", err)
		}

		return nil
	})

	if err := eg.Wait(); err != nil {
		t.Error(err)
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
