package control_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
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

	os.Exit(m.Run())
}

func TestConnectorLocalhost(t *testing.T) {
	peer1, conn1 := newPeer(t, "peer1")
	peer2, conn2 := newPeer(t, "peer2")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stream1, stream2 http3platform.Stream
	var eg errgroup.Group
	eg.Go(func() (err error) {
		stream1, err = peer1.Connect(ctx, conn2.LocalAddr().String(), "127.0.0.1:8001")
		return
	})
	eg.Go(func() (err error) {
		stream2, err = peer2.Connect(ctx, conn1.LocalAddr().String(), "127.0.0.1:8002")
		return
	})

	if err := eg.Wait(); err != nil {
		t.Fatalf("wait: %s", err)
	}

	runConnTest(t, stream1, stream2)
}

func newPeer(t *testing.T, name string) (*control.Connector, net.PacketConn) {
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
	connector := control.NewConnector(transport, tlsConfig, control.WithLogger(logger))
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

// TODO: make test working via CMD, then try to make test working via Dockerfile,
// so it is possible to run all of the locally without test helper scripts.

func TestConnectorLocal(t *testing.T) {
	peer1Cmd := exec.Command("./peer")
	peer1Cmd.Env = []string{
		"LISTEN_ADDRESS=127.0.0.1:8001",
		"PEER_PUBLIC_ADDRESS=127.0.0.1:8002", // peer2 address
		"PEER_PRIVATE_ADDRESS=127.0.0.1:8003",
		"TLS_IPS=127.0.0.1",
		"MODE=client",
	}
	peer1Cmd.Stdout = os.Stdout
	peer1Cmd.Stderr = os.Stderr

	peer2Cmd := exec.Command("./peer")
	peer2Cmd.Env = []string{
		"LISTEN_ADDRESS=127.0.0.1:8002",      // public address
		"PEER_PUBLIC_ADDRESS=127.0.0.1:8001", // peer1 address
		"PEER_PRIVATE_ADDRESS=127.0.0.1:8004",
		"TLS_IPS=127.0.0.1",
		"MODE=server",
	}
	peer2Cmd.Stdout = os.Stdout
	peer2Cmd.Stderr = os.Stderr

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
		gont.NewInterface("veth0", sw1,
			gontops.AddressIP("10.0.1.2/24")))
	if err != nil {
		t.Fatalf("create host1: %v", err)
	}

	peer2, err := n.AddHost("peer2",
		gont.NewInterface("veth0", sw1,
			gontops.AddressIP("10.0.1.3/24")))
	if err != nil {
		t.Fatalf("create host2: %v", err)
	}

	_, err = peer1.Ping(peer2)
	if err != nil {
		t.Fatalf("Failed to ping peer1 -> peer2: %v", err)
	}

	peer1Cmd := peer1.Command("./peer",
		cmdops.Envs([]string{
			"LISTEN_ADDRESS=:8000",
			"PEER_PUBLIC_ADDRESS=10.0.1.3:8000",  // peer2 address
			"PEER_PRIVATE_ADDRESS=10.0.1.3:8000", // peer2 address
			"TLS_IPS=10.0.1.2",
			"MODE=client",
		}),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

	peer2Cmd := peer2.Command("./peer",
		cmdops.Envs([]string{
			"LISTEN_ADDRESS=:8000",
			"PEER_PUBLIC_ADDRESS=10.0.1.2:8000",  // peer1 address
			"PEER_PRIVATE_ADDRESS=10.0.1.2:8000", // peer1 address
			"TLS_IPS=10.0.1.3",
			"MODE=server",
		}),
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
//	peer1 <-> sw1 <-> nat1 <-> sw1 <-> peer2
func TestConnectorNAT(t *testing.T) {
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
		t.Fatalf("create host client: %v", err)
	}

	peer2, err := n.AddHost("peer2",
		gontops.DefaultGatewayIP("10.0.2.1"),
		gont.NewInterface("veth0", sw2,
			gontops.AddressIP("10.0.2.2/24")))
	if err != nil {
		t.Fatalf("create host server: %v", err)
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

	peer1Cmd := peer1.Command("./peer",
		cmdops.Envs([]string{
			"LISTEN_ADDRESS=:8000",
			"PEER_PUBLIC_ADDRESS=10.0.1.1:8000", // public address of peer2, as seen through NAT
			"PEER_PRIVATE_ADDRESS=10.0.2.2:8000",
			"TLS_IPS=10.0.1.2,10.0.2.1",
			"MODE=client",
		}),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

	peer2Cmd := peer2.Command("./peer",
		cmdops.Envs([]string{
			"LISTEN_ADDRESS=:8000",
			"PEER_PUBLIC_ADDRESS=10.0.2.1:8000", // public address of peer1, as seen trough NAT
			"PEER_PRIVATE_ADDRESS=10.0.1.2:8000",
			"TLS_IPS=10.0.2.2,10.0.1.1",
			"MODE=server",
		}),
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
