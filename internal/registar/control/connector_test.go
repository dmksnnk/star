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
	"github.com/quic-go/quic-go/http3"
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

	fmt.Println("established, running tests")

	want := make([]byte, 1<<20)
	rand.Read(want)

	go func() {
		if _, err := io.Copy(stream1, bytes.NewReader(want)); err != nil {
			t.Errorf("unexpected stream1.Write error: %s", err)
		}
	}()

	data := make(chan []byte)
	go func() {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, stream2); err != nil {
			t.Errorf("unexpected stream2.Read error: %s", err)
		}
		data <- buf.Bytes()
	}()

	if got := <-data; !bytes.Equal(got, want) {
		t.Error("transmitted data differs")
	}
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
	peerCert, err := cert.NewIPCert(ca, caPrivateKey, addr.IP, privkey.Public())
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
		RootCAs:    pool,
		NextProtos: []string{http3.NextProtoH3},
	}

	quicConfig := &quic.Config{
		EnableDatagrams:      true,
		HandshakeIdleTimeout: time.Second,
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger = logger.With("connector", name)
	connector := control.NewConnector(transport, tlsConfig, quicConfig, logger)
	return connector, conn
}

func TestConnectorLocal(t *testing.T) {
	peer1Cmd := exec.Command("./peer")
	peer1Cmd.Env = []string{
		"LISTEN_ADDRESS=127.0.0.1:8001",
		"PEER_PUBLIC_ADDRESS=127.0.0.1:8002",
		"PEER_PRIVATE_ADDRESS=127.0.0.1:8003",
	}
	peer1Cmd.Stdout = os.Stdout
	peer1Cmd.Stderr = os.Stderr

	peer2Cmd := exec.Command("./peer")
	peer2Cmd.Env = []string{
		"LISTEN_ADDRESS=127.0.0.1:8002",      // public address
		"PEER_PUBLIC_ADDRESS=127.0.0.1:8001", // peer1 address
		"PEER_PRIVATE_ADDRESS=127.0.0.1:8004",
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

// TestConnectorBasic tests
// func TestConnectorBasic(t *testing.T) {
// 	n, err := gont.NewNetwork(t.Name())
// 	if err != nil {
// 		t.Fatalf("create network: %s", err)
// 	}
// 	t.Cleanup(func() {
// 		if err := n.Close(); err != nil {
// 			t.Errorf("close network: %s", err)
// 		}
// 	})

// 	sw1, err := n.AddSwitch("sw1")
// 	if err != nil {
// 		t.Fatalf("create switch sw1: %v", err)
// 	}

// 	host1, err := n.AddHost("host1",
// 		gontops.DefaultGatewayIP("10.0.1.1"),
// 		gont.NewInterface("veth0", sw1,
// 			gontops.AddressIP("10.0.1.2/24")))
// 	if err != nil {
// 		t.Fatalf("create host1: %v", err)
// 	}

// 	host2, err := n.AddHost("host2",
// 		gontops.DefaultGatewayIP("10.0.1.1"),
// 		gont.NewInterface("veth0", sw1,
// 			gontops.AddressIP("10.0.1.3/24")))
// 	if err != nil {
// 		t.Fatalf("create host2: %v", err)
// 	}

// 	_, err = host1.Ping(host2)
// 	if err != nil {
// 		t.Fatalf("Failed to ping host1 -> host2: %v", err)
// 	}

// 	serverCmd, err := host2.Start("go", "run", "./cmd/registar/...",
// 		cmdops.EnvVar("CERT_IP_ADDRESS", "10.0.1.3"),
// 		cmdops.EnvVar("LISTEN_TLS_ADDRESS", "10.0.1.3:8443"),
// 		cmdops.EnvVar("SECRET", "test_secret"),
// 		cmdops.EnvVar("LOG_LEVEL", "DEBUG"),
// 		cmdops.Stdout(os.Stdout),
// 		cmdops.Stderr(os.Stderr),
// 	)
// 	if err != nil {
// 		t.Fatalf("start registar server: %s", err)
// 	}
// 	t.Cleanup(func() {
// 		if err := serverCmd.Process.Signal(syscall.SIGINT); err != nil {
// 			t.Errorf("failed to send SIGINT to server: %v", err)
// 		}
// 		if err := serverCmd.Wait(); err != nil {
// 			t.Errorf("server exited with error: %v", err)
// 		}
// 	})

// 	time.Sleep(2 * time.Second)

// 	transport := &quic.Transport{}
// 	defer transport.Close()

// 	tlsConfig := &tls.Config{
// 		InsecureSkipVerify: true,
// 	}

// 	quicConfig := &quic.Config{
// 		EnableDatagrams: true,
// 	}

// 	connector := control.NewConnector(transport, tlsConfig, quicConfig)

// 	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
// 	defer cancel()

// 	publicAddr := netip.MustParseAddrPort("10.0.1.3:8443")
// 	privateAddr := netip.MustParseAddrPort("10.0.1.3:8443")

// 	stream, err := connector.Connect(ctx, publicAddr, privateAddr)
// 	if err != nil {
// 		t.Fatalf("connector.Connect failed: %v", err)
// 	}
// 	defer stream.Close()

// 	if stream == nil {
// 		t.Fatal("expected non-nil stream")
// 	}
// }

// func TestConnectorNATScenario(t *testing.T) {
// 	n, err := gont.NewNetwork(t.Name())
// 	if err != nil {
// 		t.Fatalf("create network: %s", err)
// 	}
// 	t.Cleanup(func() {
// 		if err := n.Close(); err != nil {
// 			t.Errorf("close network: %s", err)
// 		}
// 	})

// 	sw1, err := n.AddSwitch("sw1")
// 	if err != nil {
// 		t.Fatalf("create switch sw1: %v", err)
// 	}

// 	sw2, err := n.AddSwitch("sw2")
// 	if err != nil {
// 		t.Fatalf("create switch sw2: %v", err)
// 	}

// 	client, err := n.AddHost("client",
// 		gontops.DefaultGatewayIP("10.0.1.1"),
// 		gont.NewInterface("veth0", sw1,
// 			gontops.AddressIP("10.0.1.2/24")))
// 	if err != nil {
// 		t.Fatalf("create host client: %v", err)
// 	}

// 	server, err := n.AddHost("server",
// 		gontops.DefaultGatewayIP("10.0.2.1"),
// 		gont.NewInterface("veth0", sw2,
// 			gontops.AddressIP("10.0.2.2/24")))
// 	if err != nil {
// 		t.Fatalf("create host server: %v", err)
// 	}

// 	_, err = n.AddNAT("nat1",
// 		gont.NewInterface("veth0", sw1, gontops.SouthBound,
// 			gontops.AddressIP("10.0.1.1/24")),
// 		gont.NewInterface("veth1", sw2, gontops.NorthBound,
// 			gontops.AddressIP("10.0.2.1/24")))
// 	if err != nil {
// 		t.Fatalf("create NAT: %s", err)
// 	}

// 	_, err = client.Ping(server)
// 	if err != nil {
// 		t.Fatalf("Failed to ping client -> server: %v", err)
// 	}

// 	serverCmd, err := server.Start("go", "run", "./cmd/registar/...",
// 		cmdops.EnvVar("CERT_IP_ADDRESS", "10.0.2.2"),
// 		cmdops.EnvVar("LISTEN_TLS_ADDRESS", "10.0.2.2:8443"),
// 		cmdops.EnvVar("SECRET", "test_secret"),
// 		cmdops.EnvVar("LOG_LEVEL", "DEBUG"),
// 		cmdops.Stdout(os.Stdout),
// 		cmdops.Stderr(os.Stderr),
// 	)
// 	if err != nil {
// 		t.Fatalf("start registar server: %s", err)
// 	}
// 	t.Cleanup(func() {
// 		if err := serverCmd.Process.Signal(syscall.SIGINT); err != nil {
// 			t.Errorf("failed to send SIGINT to server: %v", err)
// 		}
// 		if err := serverCmd.Wait(); err != nil {
// 			t.Errorf("server exited with error: %v", err)
// 		}
// 	})

// 	time.Sleep(2 * time.Second)

// 	_, err = client.Run("go", "run", "./test_connector_client.go",
// 		cmdops.EnvVar("SERVER_PUBLIC", "10.0.2.2:8443"),
// 		cmdops.EnvVar("SERVER_PRIVATE", "10.0.2.2:8443"),
// 		cmdops.Stdout(os.Stdout),
// 		cmdops.Stderr(os.Stderr),
// 	)
// 	if err != nil {
// 		t.Errorf("run connector client: %s", err)
// 	}
// }

// func TestConnectorTimeout(t *testing.T) {
// 	transport := &quic.Transport{}
// 	defer transport.Close()

// 	tlsConfig := &tls.Config{
// 		InsecureSkipVerify: true,
// 	}

// 	quicConfig := &quic.Config{
// 		EnableDatagrams: true,
// 	}

// 	connector := control.NewConnector(transport, tlsConfig, quicConfig)

// 	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
// 	defer cancel()

// 	publicAddr := netip.MustParseAddrPort("192.0.2.1:8443")
// 	privateAddr := netip.MustParseAddrPort("192.0.2.1:8443")

// 	_, err := connector.Connect(ctx, publicAddr, privateAddr)
// 	if err == nil {
// 		t.Fatal("expected Connect to fail with timeout")
// 	}

// 	if ctx.Err() != context.DeadlineExceeded {
// 		t.Errorf("expected context deadline exceeded, got: %v", err)
// 	}
// }

// func TestConnectorValidation(t *testing.T) {
// 	n, err := gont.NewNetwork(t.Name())
// 	if err != nil {
// 		t.Fatalf("create network: %s", err)
// 	}
// 	t.Cleanup(func() {
// 		if err := n.Close(); err != nil {
// 			t.Errorf("close network: %s", err)
// 		}
// 	})

// 	sw1, err := n.AddSwitch("sw1")
// 	if err != nil {
// 		t.Fatalf("create switch sw1: %v", err)
// 	}

// 	host1, err := n.AddHost("host1",
// 		gontops.DefaultGatewayIP("10.0.1.1"),
// 		gont.NewInterface("veth0", sw1,
// 			gontops.AddressIP("10.0.1.2/24")))
// 	if err != nil {
// 		t.Fatalf("create host1: %v", err)
// 	}

// 	host2, err := n.AddHost("host2",
// 		gontops.DefaultGatewayIP("10.0.1.1"),
// 		gont.NewInterface("veth0", sw1,
// 			gontops.AddressIP("10.0.1.3/24")))
// 	if err != nil {
// 		t.Fatalf("create host2: %v", err)
// 	}

// 	serverWithoutDatagrams := &http3.Server{
// 		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
// 			w.WriteHeader(http.StatusOK)
// 		}),
// 	}

// 	go func() {
// 		err := host2.RunFunc(func() error {
// 			return serverWithoutDatagrams.ListenAndServeTLS("10.0.1.3:8443", "", "")
// 		})
// 		if err != nil {
// 			t.Errorf("server listen error: %v", err)
// 		}
// 	}()

// 	time.Sleep(2 * time.Second)

// 	transport := &quic.Transport{}
// 	defer transport.Close()

// 	tlsConfig := &tls.Config{
// 		InsecureSkipVerify: true,
// 	}

// 	quicConfig := &quic.Config{
// 		EnableDatagrams: false,
// 	}

// 	connector := control.NewConnector(transport, tlsConfig, quicConfig)

// 	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 	defer cancel()

// 	publicAddr := netip.MustParseAddrPort("10.0.1.3:8443")
// 	privateAddr := netip.MustParseAddrPort("10.0.1.3:8443")

// 	_, err = connector.Connect(ctx, publicAddr, privateAddr)
// 	if err == nil {
// 		t.Fatal("expected Connect to fail when datagrams are disabled")
// 	}
// }
