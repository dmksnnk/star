package p2p_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/gob"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/registar/p2p"
	"github.com/dmksnnk/star/internal/registar/p2p/p2ptest/config"
	"github.com/quic-go/quic-go"
	"go.uber.org/goleak"
	"golang.org/x/sync/errgroup"
)

var (
	ca           *x509.Certificate
	caPrivateKey crypto.PrivateKey
)

func TestMain(m *testing.M) {
	var err error
	ca, caPrivateKey, err = cert.NewCA()
	if err != nil {
		fmt.Fprintf(os.Stderr, "create CA: %s", err)
		os.Exit(1)
	}

	exitCode := m.Run()
	if exitCode == 0 {
		// something is leaking in gont
		// TODO: figure out if it gont or our code
		if err := goleak.Find(
			goleak.IgnoreAnyFunction("github.com/godbus/dbus/v5.(*Conn).inWorker"),
			goleak.IgnoreTopFunction("github.com/godbus/dbus/v5.newConn.func1"),
			goleak.IgnoreTopFunction("github.com/coreos/go-systemd/v22/dbus.(*Conn).dispatch.func1"),
		); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %s", err)
			exitCode = 1
		}
	}

	os.Exit(exitCode)
}

var localhost = netip.AddrFrom4([4]byte{127, 0, 0, 1})

func localhostAddPort(port uint16) netip.AddrPort {
	return netip.AddrPortFrom(localhost, port)
}

// TestConnectorCancelsPrivateDialWhenPublicSucceeds verifies that when public dial succeeds,
// the private dial is cancelled immediately rather than waiting for a 30s timeout.
//
// Scenario:
// - peer1 calls Connect() with different public/private addresses
// - A mock peer responds to ONE connection (public) then stops responding
// - peer1's private dial establishes QUIC but gets no handshake response
// - Without the fix: private dial waits 30s in http.ReadResponse
// - With the fix: private dial is cancelled immediately when public succeeds
func TestConnectorCancelsPrivateDialWhenPublicSucceeds(t *testing.T) {
	// Create peer1's connector
	peer1, _ := newPeer(t, "peer1")

	// Create mock peer that only responds to ONE connection (public address)
	mockPublicTransport, mockTLSConfig := newPeerTransport(t)
	mockPublicAddr := mockPublicTransport.Conn.LocalAddr().(*net.UDPAddr).AddrPort()

	// Create a separate transport for private address that accepts connections
	// but never responds to handshakes (simulating peer that already returned from Connect)
	mockPrivateTransport, _ := newPeerTransport(t)
	mockPrivateAddr := mockPrivateTransport.Conn.LocalAddr().(*net.UDPAddr).AddrPort()

	// Start mock peer's listener that responds to exactly one handshake
	mockListener, err := mockPublicTransport.ListenEarly(mockTLSConfig, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatalf("mock listen: %s", err)
	}

	// Start a "dead" listener on private address that accepts QUIC but never responds to handshakes
	deadListener, err := mockPrivateTransport.ListenEarly(mockTLSConfig, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatalf("dead listen: %s", err)
	}

	// Accept connections on private address but never respond (simulates peer that stopped)
	go func() {
		for {
			conn, err := deadListener.Accept(context.Background())
			if err != nil {
				return // listener closed
			}
			// Accept the QUIC connection but never respond to the handshake stream
			// This simulates a peer that established QUIC but then went away
			_ = conn
		}
	}()

	// Mock peer: accept one connection and respond to handshake
	go func() {
		defer mockListener.Close()
		defer deadListener.Close()

		conn, err := mockListener.Accept(context.Background())
		if err != nil {
			t.Errorf("mock accept: %s", err)
			return
		}

		// Accept handshake stream and respond
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			t.Errorf("mock accept stream: %s", err)
			return
		}

		// Read the HTTP request (just drain it)
		buf := make([]byte, 1024)
		stream.Read(buf)

		// Send OK response
		resp := "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"
		stream.Write([]byte(resp))
		stream.Close()

		// Don't accept any more connections - simulating that we returned from Connect()
	}()

	// Use a short timeout - if the bug exists (private dial blocks for 30s),
	// this test will fail with context deadline exceeded
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// peer1 connects with different public/private addresses
	// Public will succeed, private will establish QUIC but get no handshake response
	conn, err := peer1.Connect(ctx, mockPublicAddr, mockPrivateAddr)
	if err != nil {
		t.Fatalf("connect failed: %s", err)
	}

	if conn == nil {
		t.Fatal("expected connection to be established")
	}
	conn.CloseWithError(0, "test done")
}

// TestConnectorLocal runs two peers locally and connects them directly.
func TestConnectorLocal(t *testing.T) {
	peer1, packetConn1 := newPeer(t, "peer1")
	peer2, packetConn2 := newPeer(t, "peer2")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var quicConn1, quicConn2 *quic.Conn
	var eg errgroup.Group
	eg.Go(func() (err error) {
		addr := packetConn2.LocalAddr().(*net.UDPAddr).AddrPort()
		quicConn1, err = peer1.Connect(ctx, addr, localhostAddPort(8001))

		return
	})
	eg.Go(func() (err error) {
		addr := packetConn1.LocalAddr().(*net.UDPAddr).AddrPort()
		quicConn2, err = peer2.Connect(ctx, addr, localhostAddPort(8002))

		return
	})

	if err := eg.Wait(); err != nil {
		t.Fatalf("establish connection: %s", err)
	}

	// stream is accpected with first data read, so we need to send data from one side (like handshake)
	var stream1, stream2 *quic.Stream
	eg = errgroup.Group{}
	eg.Go(func() (err error) {
		stream1, err = quicConn1.AcceptStream(ctx)
		if err != nil {
			return
		}

		buf := make([]byte, 5)
		_, err = stream1.Read(buf)
		if err != nil {
			return
		}
		if !bytes.Equal(buf, []byte("hello")) {
			err = fmt.Errorf("unexpected data: %q", buf)
			return
		}

		return
	})

	eg.Go(func() (err error) {
		stream2, err = quicConn2.OpenStreamSync(ctx)
		if err != nil {
			return
		}

		stream2.Write([]byte("hello"))

		return
	})
	if err := eg.Wait(); err != nil {
		t.Fatalf("open/accept stream: %s", err)
	}

	runConnTest(t, stream1, stream2)
}

func newPeer(t *testing.T, name string) (*p2p.Connector, net.PacketConn) {
	transport, tlsConfig := newPeerTransport(t)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger = logger.With("connector", name)
	connector := p2p.NewConnector(transport, tlsConfig, p2p.WithLogger(logger))
	return connector, transport.Conn
}

func newPeerTransport(t *testing.T) (*quic.Transport, *tls.Config) {
	t.Helper()

	addr := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0,
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen UDP: %s", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close UDP conn: %s", err)
		}
	})

	transport := &quic.Transport{
		Conn: conn,
	}
	t.Cleanup(func() {
		if err := transport.Close(); err != nil {
			t.Errorf("close QUIC transport: %s", err)
		}
	})

	privkey, err := cert.NewPrivateKey()
	if err != nil {
		t.Fatal("create server private key:", err)
	}
	peerCert, err := cert.NewIPCert(ca, caPrivateKey, privkey.Public(), addr.IP)
	if err != nil {
		t.Fatalf("create IP cert: %s", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{peerCert},
				PrivateKey:  privkey,
			},
		},
		RootCAs: pool,
	}

	return transport, tlsConfig
}

func runConnTest(t *testing.T, conn1, conn2 io.ReadWriteCloser) {
	t.Helper()
	want := make([]byte, 1<<20)
	rand.Read(want)

	go func() {
		if _, err := io.Copy(conn1, bytes.NewReader(want)); err != nil {
			t.Errorf("unexpected conn1.Write error: %s", err)
		}
		if err := conn1.Close(); err != nil {
			t.Errorf("unexpected conn1.Close error: %s", err)
		}
	}()

	data := make(chan []byte)
	go func() {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, conn2); err != nil {
			t.Errorf("unexpected conn2.Read error: %s", err)
		}
		if err := conn2.Close(); err != nil {
			t.Errorf("unexpected conn2.Close error: %s", err)
		}
		data <- buf.Bytes()
	}()

	if got := <-data; !bytes.Equal(got, want) {
		t.Error("transmitted data differs")
	}
}

func newCertConfig(t *testing.T, ips ...net.IP) config.Cert {
	privkey, err := cert.NewPrivateKey()
	if err != nil {
		t.Fatal("create server private key:", err)
	}
	certBytes, err := cert.NewIPCert(ca, caPrivateKey, privkey.Public(), ips...)
	if err != nil {
		t.Fatalf("create IP cert: %s", err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(privkey)
	if err != nil {
		t.Fatalf("marshal private key: %s", err)
	}

	return config.Cert{
		CACert:     ca.Raw,
		Cert:       certBytes,
		PrivateKey: privBytes,
	}
}

func TestConnectorLocalhost(t *testing.T) {
	peerBin := buildPeer(t)

	cfg1 := config.Config{
		Name:               "peer1",
		ListenAddress:      localhostAddPort(8001),
		PeerPublicAddress:  localhostAddPort(8002), // peer2 address
		PeerPrivateAddress: localhostAddPort(8002),
		Mode:               "client",
		Cert:               newCertConfig(t, net.ParseIP("127.0.0.1")),
	}
	peer1Cmd := exec.Command(peerBin)
	peer1Cmd.Stdout = os.Stdout
	peer1Cmd.Stderr = os.Stderr
	peer1Cmd.Stdin = stdinConfig(t, cfg1)

	cfg2 := config.Config{
		Name:               "peer2",
		ListenAddress:      localhostAddPort(8002), // public address
		PeerPublicAddress:  localhostAddPort(8001), // peer1 address
		PeerPrivateAddress: localhostAddPort(8001),
		Mode:               "server",
		Cert:               newCertConfig(t, net.ParseIP("127.0.0.1")),
	}
	peer2Cmd := exec.Command(peerBin)
	peer2Cmd.Stdout = os.Stdout
	peer2Cmd.Stderr = os.Stderr
	peer2Cmd.Stdin = stdinConfig(t, cfg2)

	var eg errgroup.Group
	eg.Go(func() error {
		return peer1Cmd.Run()
	})
	eg.Go(func() error {
		return peer2Cmd.Run()
	})

	if err := eg.Wait(); err != nil {
		t.Errorf("failed: %v", err)
	}
}

const peerPkg = "github.com/dmksnnk/star/internal/registar/p2p/p2ptest"

func buildPeer(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "peer")

	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", path, peerPkg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build peer: %s:\n %s", err, out)
	}

	return path
}

func stdinConfig[T any](t *testing.T, cfg T) io.Reader {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&cfg); err != nil {
		t.Fatalf("failed to encode config: %s", err)
	}

	return &buf
}
