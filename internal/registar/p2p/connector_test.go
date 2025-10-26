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

	gont "cunicu.li/gont/v2/pkg"
	gontops "cunicu.li/gont/v2/pkg/options"
	cmdops "cunicu.li/gont/v2/pkg/options/cmd"
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
		if err := goleak.Find(); err != nil {
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

	// stream is accpected with first data read, so we need to send data from one side
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

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger = logger.With("connector", name)
	connector := p2p.NewConnector(transport, tlsConfig, p2p.WithLogger(logger))
	return connector, conn
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

// TestConnectorSingleSwitch tests connection over single switch.
//
//	peer1 <-> sw1 <-> peer2
func TestConnectorSingleSwitch(t *testing.T) {
	peerPath := buildPeer(t)

	n, err := gont.NewNetwork(t.Name())
	if err != nil {
		t.Fatalf("create network: %s", err)
	}
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Errorf("close network: %s", err)
		}
	})

	sw1, err := n.AddSwitch("sw1")
	if err != nil {
		t.Fatalf("create switch sw1: %v", err)
	}

	peer1, err := n.AddHost("peer1",
		gontops.DefaultGatewayIP("10.0.1.1"),
		gont.NewInterface("veth0", sw1,
			gontops.AddressIP("10.0.1.2/24")))
	if err != nil {
		t.Fatalf("create peer1: %v", err)
	}

	peer2, err := n.AddHost("peer2",
		gontops.DefaultGatewayIP("10.0.1.1"),
		gont.NewInterface("veth0", sw1,
			gontops.AddressIP("10.0.1.3/24")))
	if err != nil {
		t.Fatalf("create peer2: %v", err)
	}

	_, err = peer1.Ping(peer2)
	if err != nil {
		t.Fatalf("Failed to ping peer1 -> peer2: %v", err)
	}

	cfg1 := config.Config{
		ListenAddress:      netip.MustParseAddrPort("10.0.1.2:8000"),
		PeerPublicAddress:  netip.MustParseAddrPort("10.0.1.3:8000"), // peer2 address
		PeerPrivateAddress: netip.MustParseAddrPort("10.0.1.3:8000"), // peer2 address
		Mode:               "client",
		Cert:               newCertConfig(t, net.ParseIP("10.0.1.2"), net.ParseIP("10.0.0.1")),
	}
	peer1Cmd := peer1.Command(peerPath,
		cmdops.Stdin(stdinConfig(t, cfg1)),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

	cfg2 := config.Config{
		ListenAddress:      netip.MustParseAddrPort("10.0.1.3:8000"),
		PeerPublicAddress:  netip.MustParseAddrPort("10.0.1.2:8000"), // peer1 address
		PeerPrivateAddress: netip.MustParseAddrPort("10.0.1.2:8000"), // peer1 address
		Cert:               newCertConfig(t, net.ParseIP("10.0.1.3"), net.ParseIP("10.0.1.1")),
		Mode:               "server",
	}
	peer2Cmd := peer2.Command(peerPath,
		cmdops.Stdin(stdinConfig(t, cfg2)),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

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

// TestConnectorNAT
//
//	peer1 <-> sw1 <-> nat1 <-> sw2 <-> peer2
func TestConnectorNAT(t *testing.T) {
	peerPath := buildPeer(t)

	n, err := gont.NewNetwork(t.Name())
	if err != nil {
		t.Fatalf("create network: %s", err)
	}
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Errorf("close network: %s", err)
		}
	})

	sw1, err := n.AddSwitch("sw1")
	if err != nil {
		t.Fatalf("create switch sw1: %v", err)
	}

	sw2, err := n.AddSwitch("sw2")
	if err != nil {
		t.Fatalf("create switch sw2: %v", err)
	}

	peer1, err := n.AddHost("peer1",
		gontops.DefaultGatewayIP("10.0.1.1"),
		gont.NewInterface("veth0", sw1,
			gontops.AddressIP("10.0.1.2/24")))
	if err != nil {
		t.Fatalf("create peer1 host: %v", err)
	}

	peer2, err := n.AddHost("peer2",
		gontops.DefaultGatewayIP("10.0.2.1"),
		gont.NewInterface("veth0", sw2,
			gontops.AddressIP("10.0.2.2/24")))
	if err != nil {
		t.Fatalf("create peer2 host: %v", err)
	}

	_, err = n.AddNAT("nat1",
		gont.NewInterface("veth0", sw1, gontops.SouthBound,
			gontops.AddressIP("10.0.1.1/24")),
		gont.NewInterface("veth1", sw2, gontops.NorthBound,
			gontops.AddressIP("10.0.2.1/24")))
	if err != nil {
		t.Fatalf("create NAT: %s", err)
	}

	_, err = peer1.Ping(peer2)
	if err != nil {
		t.Fatalf("Failed to ping peer1 -> peer2: %v", err)
	}

	cfg1 := config.Config{
		ListenAddress:      netip.AddrPortFrom(netip.IPv4Unspecified(), 8000),
		PeerPublicAddress:  netip.MustParseAddrPort("10.0.1.1:8000"), // public address of peer2, as seen through NAT
		PeerPrivateAddress: netip.MustParseAddrPort("10.0.2.2:8000"),
		Mode:               "client",
		Cert:               newCertConfig(t, net.ParseIP("10.0.1.2"), net.ParseIP("10.0.2.1")),
	}
	peer1Cmd := peer1.Command(peerPath,
		cmdops.Stdin(stdinConfig(t, cfg1)),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

	cfg2 := config.Config{
		ListenAddress:      netip.AddrPortFrom(netip.IPv4Unspecified(), 8000),
		PeerPublicAddress:  netip.MustParseAddrPort("10.0.2.1:8000"), // public address of peer1, as seen trough NAT
		PeerPrivateAddress: netip.MustParseAddrPort("10.0.1.2:8000"),
		Mode:               "server",
		Cert:               newCertConfig(t, net.ParseIP("10.0.2.2"), net.ParseIP("10.0.1.1")),
	}
	peer2Cmd := peer2.Command(peerPath,
		cmdops.Stdin(stdinConfig(t, cfg2)),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

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
