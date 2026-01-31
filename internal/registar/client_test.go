package registar_test

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

func TestRegisterHost(t *testing.T) {
	secret := []byte("secret")
	reg := newFakeRegistar()
	srv := integrationtest.NewServer(t, reg, secret)
	key := auth.NewKey()

	token := auth.NewToken(key, secret)

	tr := &quic.Transport{Conn: integrationtest.NewLocalUDPConn(t)}
	cc := registar.ClientConfig{
		TLSConfig: srv.TLSConfig(),
	}
	clientConn, _, err := cc.Host(context.TODO(), tr, srv.URL(), token)
	if err != nil {
		t.Fatalf("register client: %s", err)
	}
	defer func() {
		if err := clientConn.CloseWithError(0, ""); err != nil {
			t.Errorf("close client conn: %s", err)
		}
	}()

	registarConn, ok := reg.host(key)
	if !ok {
		t.Fatalf("host not registered")
	}

	// important to pass registarConn first, as it opens the stream
	testPipeConn(t, registarConn, clientConn)
}

func TestRegisterJoin(t *testing.T) {
	secret := []byte("secret")
	reg := newFakeRegistar()
	srv := integrationtest.NewServer(t, reg, secret)
	key := auth.NewKey()

	token := auth.NewToken(key, secret)

	tr := &quic.Transport{Conn: integrationtest.NewLocalUDPConn(t)}
	cc := registar.ClientConfig{
		TLSConfig: srv.TLSConfig(),
	}
	clientConn, _, err := cc.Join(context.TODO(), tr, srv.URL(), token)
	if err != nil {
		t.Fatalf("Join: %s", err)
	}
	defer func() {
		if err := clientConn.CloseWithError(0, ""); err != nil {
			t.Errorf("close client conn: %s", err)
		}
	}()

	registarConn, ok := reg.peer(key)
	if !ok {
		t.Fatalf("peer not registered")
	}

	// important to pass registarConn first, as it opens the stream
	testPipeConn(t, registarConn, clientConn)
}

type fakeRegistar struct {
	mux   sync.Mutex
	hosts map[auth.Key]*quic.Conn
	peers map[auth.Key]*quic.Conn
}

func newFakeRegistar() *fakeRegistar {
	return &fakeRegistar{
		hosts: make(map[auth.Key]*quic.Conn),
		peers: make(map[auth.Key]*quic.Conn),
	}
}

func (f *fakeRegistar) NewSessionCert(key auth.Key, csr *x509.CertificateRequest) (*x509.Certificate, *x509.Certificate, error) {
	ca, caPrivKey, err := cert.NewCA()
	if err != nil {
		return nil, nil, fmt.Errorf("create CA: %w", err)
	}

	privkey, err := cert.NewPrivateKey()
	if err != nil {
		return nil, nil, fmt.Errorf("create private key: %w", err)
	}

	peerCert, err := cert.NewIPCert(ca, caPrivKey, privkey.Public(), net.IPv4(127, 0, 0, 1))
	if err != nil {
		return nil, nil, fmt.Errorf("create IP cert: %w", err)
	}

	x509PeerCert, err := x509.ParseCertificate(peerCert)
	if err != nil {
		return nil, nil, fmt.Errorf("parse peer certificate: %w", err)
	}

	return ca, x509PeerCert, nil
}

func (f *fakeRegistar) Host(ctx context.Context, key auth.Key, stream *quic.Conn, addrs registar.AddrPair) error {
	f.mux.Lock()
	defer f.mux.Unlock()

	f.hosts[key] = stream

	return nil
}

func (f *fakeRegistar) Join(ctx context.Context, key auth.Key, stream *quic.Conn, addrs registar.AddrPair) error {
	f.mux.Lock()
	defer f.mux.Unlock()

	f.peers[key] = stream
	return nil
}

func (f *fakeRegistar) host(key auth.Key) (*quic.Conn, bool) {
	f.mux.Lock()
	defer f.mux.Unlock()

	c, ok := f.hosts[key]
	return c, ok
}

func (f *fakeRegistar) peer(key auth.Key) (*quic.Conn, bool) {
	f.mux.Lock()
	defer f.mux.Unlock()

	c, ok := f.peers[key]
	return c, ok
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
