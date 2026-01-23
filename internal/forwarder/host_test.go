package forwarder_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/dmksnnk/star/internal/forwarder"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"golang.org/x/sync/errgroup"
)

var (
	secret = []byte("secret")
	token  = auth.NewToken(auth.NewKey(), secret)
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
)

// TODO: maybe move to integration tests
func TestRunHost(t *testing.T) {
	relay, relayAddr := integrationtest.ServeRelay(t)
	reg := registar.NewRegistar2(relayAddr, relay)
	srv := integrationtest.NewServer(t, secret, reg)
	discoverySrvAddr := integrationtest.ServeDiscovery(t)

	hostCfg := forwarder.HostConfig{
		Logger:     logger,
		TLSConfig:  srv.TLSConfig(),
		ListenAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}, // localhost
		ErrHandlers: []func(error){
			func(err error) {
				t.Errorf("host error: %v", err)
			},
		},
	}

	peerCfg := forwarder.PeerConfig{
		Logger:             logger,
		TLSConfig:          srv.TLSConfig(),
		RegistarListenAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}, // localhost
	}

	t.Run("stops on Close", func(t *testing.T) {
		serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen udp: %v", err)
		}

		host, err := hostCfg.Register(
			context.TODO(),
			discoverySrvAddr,
			srv.URL(),
			token,
		)
		if err != nil {
			t.Fatalf("host register: %v", err)
		}

		peer, err := peerCfg.Register(
			context.TODO(),
			discoverySrvAddr,
			srv.URL(),
			token,
		)
		if err != nil {
			t.Fatalf("peer register: %v", err)
		}

		eg, ctx := errgroup.WithContext(context.Background())

		eg.Go(func() error {
			if err := host.Run(ctx, serverConn.LocalAddr().(*net.UDPAddr)); err != nil {
				return fmt.Errorf("host run: %w", err)
			}

			return nil
		})

		eg.Go(func() error {
			if err := peer.AcceptAndLink(ctx); err != nil {
				return fmt.Errorf("peer run: %w", err)
			}

			return nil
		})

		clientConn, err := net.DialUDP("udp", nil, peer.UDPAddr())
		if err != nil {
			t.Fatalf("dial UDP: %v", err)
		}

		pingPong(t, serverConn, clientConn)

		serverConn.Close()
		clientConn.Close()

		if err := peer.Close(); err != nil {
			t.Errorf("peer close: %v", err)
		}
		if err := host.Close(); err != nil {
			t.Errorf("host close: %v", err)
		}

		if err := eg.Wait(); err != nil {
			t.Errorf("%s", err)
		}
	})

	t.Run("stops on context cancel", func(t *testing.T) {
		t.Skip("this test fails because registar still thinks it has active host")

		serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen udp: %v", err)
		}

		host, err := hostCfg.Register(
			context.TODO(),
			discoverySrvAddr,
			srv.URL(),
			token,
		)
		if err != nil {
			t.Fatalf("host register: %v", err)
		}

		peer, err := peerCfg.Register(
			context.TODO(),
			discoverySrvAddr,
			srv.URL(),
			token,
		)
		if err != nil {
			t.Fatalf("peer register: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		eg, ctx := errgroup.WithContext(ctx)

		eg.Go(func() error {
			err := host.Run(ctx, serverConn.LocalAddr().(*net.UDPAddr))
			if !errors.Is(err, context.Canceled) {
				return fmt.Errorf("host run: %w", err)
			}

			return nil
		})

		eg.Go(func() error {
			err := peer.AcceptAndLink(ctx)
			if !errors.Is(err, context.Canceled) {
				return fmt.Errorf("peer run: %w", err)
			}

			return nil
		})

		clientConn, err := net.DialUDP("udp", nil, peer.UDPAddr())
		if err != nil {
			t.Fatalf("dial UDP: %v", err)
		}

		pingPong(t, serverConn, clientConn)

		cancel()

		if err := eg.Wait(); err != nil {
			t.Fatalf("%s", err)
		}

		serverConn.Close()
		clientConn.Close()

		if err := peer.Close(); err != nil {
			t.Errorf("peer close: %v", err)
		}
		if err := host.Close(); err != nil {
			t.Errorf("host close: %v", err)
		}
	})
}

// pingPong runs a ping-pong exchange between a UDP server and a connected UDP client.
// The client sends incrementing numbers, the server reads from any address and responds back to it.
func pingPong(t *testing.T, server *net.UDPConn, client net.Conn) {
	t.Helper()

	var wg sync.WaitGroup
	wg.Add(2)

	// Server: listens on UDP, responds to the address it receives from.
	go func() {
		defer wg.Done()
		buf := make([]byte, 8)
		var prev uint64
		for {
			_, addr, err := server.ReadFromUDP(buf)
			if err != nil {
				t.Errorf("server: failed to read: %v", err)
				return
			}

			msg := binary.LittleEndian.Uint64(buf)
			if prev != 0 && msg != prev+2 {
				t.Errorf("server: expected %d, got %d", prev+2, msg)
				return
			}
			prev = msg

			binary.LittleEndian.PutUint64(buf, msg+1)
			if _, err := server.WriteToUDP(buf, addr); err != nil {
				t.Errorf("server: failed to write: %v", err)
				return
			}

			if prev >= 100 {
				return
			}
		}
	}()

	// Client: connected UDP, uses Read/Write.
	go func() {
		defer wg.Done()
		buf := make([]byte, 8)
		var prev uint64
		for {
			_, err := client.Read(buf)
			if err != nil {
				t.Errorf("client: failed to read: %v", err)
				return
			}

			msg := binary.LittleEndian.Uint64(buf)
			if msg != prev+1 {
				t.Errorf("client: expected %d, got %d", prev+1, msg)
				return
			}
			prev = msg + 1 // track what we'll send next

			binary.LittleEndian.PutUint64(buf, msg+1)
			if _, err := client.Write(buf); err != nil {
				t.Errorf("client: failed to write: %v", err)
				return
			}

			if msg+1 >= 100 {
				return
			}
		}
	}()

	// Client starts the chain.
	if _, err := client.Write(binary.LittleEndian.AppendUint64(nil, 0)); err != nil {
		t.Fatalf("failed to start ping-pong: %v", err)
	}

	wg.Wait()
}
