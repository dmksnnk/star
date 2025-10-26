package registar_test

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"sync"
	"testing"

	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

func TestRegisterHost(t *testing.T) {
	secret := []byte("secret")
	reg := newFakeRegistar()
	srv := integrationtest.NewServer(t, secret, reg)
	key := auth.NewKey()

	client := integrationtest.NewClient(t, &quic.Transport{Conn: integrationtest.NewLocalUDPConn(t)}, srv, secret, key)

	addrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("192.0.0.1:1234"),
		Private: netip.MustParseAddrPort("192.0.0.2:5678"),
	}

	connA, err := client.Host(context.TODO(), addrs)
	if err != nil {
		t.Fatalf("Host: %s", err)
	}
	defer func() {
		connA.CancelRead(0)
		connA.CancelWrite(0)
	}()

	connB, ok := reg.host(key)
	if !ok {
		t.Fatalf("host not registered")
	}

	defer func() {
		connB.CancelRead(0)
		connB.CancelWrite(0)
	}()

	testPipeConn(t, connA, connB)
}

func TestRegisterJoin(t *testing.T) {
	secret := []byte("secret")
	reg := newFakeRegistar()
	srv := integrationtest.NewServer(t, secret, reg)
	key := auth.NewKey()

	client := integrationtest.NewClient(t, &quic.Transport{Conn: integrationtest.NewLocalUDPConn(t)}, srv, secret, key)

	addrs := registar.AddrPair{
		Public:  netip.MustParseAddrPort("192.0.0.1:1234"),
		Private: netip.MustParseAddrPort("192.0.0.2:5678"),
	}

	connB, err := client.Join(context.TODO(), addrs)
	if err != nil {
		t.Fatalf("Join: %s", err)
	}
	defer func() {
		connB.CancelRead(0)
		connB.CancelWrite(0)
	}()

	connA, ok := reg.peer(key)
	if !ok {
		t.Fatalf("peer not registered")
	}

	defer func() {
		connA.CancelRead(0)
		connA.CancelWrite(0)
	}()

	testPipeConn(t, connA, connB)
}

type fakeRegistar struct {
	mux   sync.Mutex
	hosts map[auth.Key]*http3.Stream
	peers map[auth.Key]*http3.Stream
}

func (f *fakeRegistar) Host(ctx context.Context, key auth.Key, streamer http3.HTTPStreamer, addrs registar.AddrPair) error {
	f.mux.Lock()
	defer f.mux.Unlock()

	f.hosts[key] = streamer.HTTPStream()

	return nil
}

func (f *fakeRegistar) Join(ctx context.Context, key auth.Key, streamer http3.HTTPStreamer, addrs registar.AddrPair) error {
	f.mux.Lock()
	defer f.mux.Unlock()

	f.peers[key] = streamer.HTTPStream()

	return nil
}

func (f *fakeRegistar) host(key auth.Key) (*http3.Stream, bool) {
	f.mux.Lock()
	defer f.mux.Unlock()

	c, ok := f.hosts[key]
	return c, ok
}

func (f *fakeRegistar) peer(key auth.Key) (*http3.Stream, bool) {
	f.mux.Lock()
	defer f.mux.Unlock()

	c, ok := f.peers[key]
	return c, ok
}

func newFakeRegistar() *fakeRegistar {
	return &fakeRegistar{
		hosts: make(map[auth.Key]*http3.Stream),
		peers: make(map[auth.Key]*http3.Stream),
	}
}

func testPipeConn(t *testing.T, a, b io.ReadWriter) {
	t.Helper()

	msgA := []byte("from A to B")
	msgB := []byte("from B to A")

	var eg errgroup.Group
	eg.Go(func() error {
		buf := make([]byte, len(msgB))
		_, err := a.Read(buf)
		if err != nil {
			return fmt.Errorf("A read: %w", err)
		}

		if string(buf) != string(msgB) {
			t.Errorf("A expected %q, got %q", msgB, buf)
		}

		return nil
	})

	eg.Go(func() error {
		buf := make([]byte, len(msgA))
		_, err := b.Read(buf)
		if err != nil {
			return fmt.Errorf("B read: %w", err)
		}

		if string(buf) != string(msgA) {
			t.Errorf("B expected %q, got %q", msgA, buf)
		}

		return nil
	})

	if _, err := a.Write(msgA); err != nil {
		t.Errorf("A write error: %v", err)
	}

	if _, err := b.Write(msgB); err != nil {
		t.Errorf("B write error: %v", err)
	}

	if err := eg.Wait(); err != nil {
		t.Error(err)
	}
}
