package forwarder_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/forwarder"
	"github.com/dmksnnk/star/internal/platform/udp"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

var (
	secret = []byte("secret")
	token  = auth.NewToken(auth.NewKey(), secret)
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
)

func TestRunHost(t *testing.T) {
	relay, relayAddr := integrationtest.ServeRelay(t)
	reg := registar.NewRegistar2(relayAddr, relay)
	srv := integrationtest.NewServer(t, secret, reg)
	discoverySrvAddr := integrationtest.ServeDiscovery(t)

	gamePort := runGameServer(t)

	hostUDPConn, hostPublicAddr, err := forwarder.DiscoverConn(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, discoverySrvAddr)
	if err != nil {
		t.Fatalf("DiscoverConn: %v", err)
	}

	hostTransport := &quic.Transport{
		Conn: hostUDPConn,
	}

	ctx, cancel := context.WithCancel(context.Background())
	eg, ctx := errgroup.WithContext(ctx)

	hostCfg := forwarder.HostConfig{
		Logger:    logger,
		TLSConfig: srv.TLSConfig(),
		ErrHandlers: []func(error){
			func(err error) {
				t.Errorf("host error: %v", err)
			},
		},
	}

	host, err := hostCfg.Register(
		ctx,
		hostTransport,
		srv.URL(),
		hostPublicAddr,
		token,
	)
	if err != nil {
		t.Fatalf("host register: %v", err)
	}

	eg.Go(func() error {
		if err := host.Run(ctx, gamePort); err != nil {
			return fmt.Errorf("host run: %w", err)
		}

		return nil
	})

	peerUDPConn, peerPublicAddr, err := forwarder.DiscoverConn(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, discoverySrvAddr)
	if err != nil {
		t.Fatalf("DiscoverConn: %v", err)
	}

	peerTransport := &quic.Transport{
		Conn: peerUDPConn,
	}

	peerListener, err := udp.Listen(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	defer peerListener.Close()

	peerCfg := forwarder.PeerConfig{
		Logger:    logger,
		TLSConfig: srv.TLSConfig(),
	}
	peer, err := peerCfg.Register(
		ctx,
		peerTransport,
		srv.URL(),
		peerPublicAddr,
		token,
	)
	if err != nil {
		t.Fatalf("peer register: %v", err)
	}

	eg.Go(func() error {
		if err := peer.AcceptAndLink(ctx, peerListener); err != nil {
			return fmt.Errorf("peer run: %w", err)
		}

		return nil
	})

	// act as a game client
	gameConn, err := net.DialUDP("udp", nil, peerListener.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial UDP: %v", err)
	}

	var received []byte
	buf := make([]byte, 1500)
	message := []byte("hello from game client")
	for range 5 {
		if _, err := gameConn.Write(message); err != nil {
			t.Fatalf("write to game UDP: %v", err)
		}

		gameConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))

		n, err := gameConn.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				slog.Info("game client read timeout, retrying...")
				continue
			}

			t.Fatalf("read from game UDP: %v", err)
		}

		received = buf[:n]
		break
	}

	if string(received) != string(message) {
		t.Fatalf("unexpected message from game server: got %q, want %q", string(received), string(message))
	}

	slog.Info("game client received reply", "message", string(received))
	cancel()
	peer.Close()
	host.Close()

	if err := eg.Wait(); err != nil {
		t.Fatalf("%s", err)
	}
}

func runGameServer(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}

	port := conn.LocalAddr().(*net.UDPAddr).Port

	t.Logf("game server listening on port %d", port)

	var eg errgroup.Group
	eg.Go(func() error {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return err
			}

			t.Logf("game server received %d bytes from %s: %s", n, addr, string(buf[:n]))

			if _, err := conn.WriteToUDP(buf[:n], addr); err != nil {
				return err
			}
		}
	})

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close game server: %v", err)
		}
	})

	return port
}
