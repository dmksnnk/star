package forwarder

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/platform/udp"
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
		if err := goleak.Find(); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %s", err)
			exitCode = 1
		}
	}

	os.Exit(exitCode)
}

func TestLink(t *testing.T) {
	udpSrv, udpClient := localUDPPipe(t)
	quicSrv, quicClient := localQUICPipe(t)

	ctx, cancel := context.WithCancel(context.Background())

	var eg errgroup.Group
	eg.Go(func() error {
		return link(ctx, udpSrv, quicSrv)
	})

	if _, err := udpClient.Write([]byte("hello")); err != nil {
		t.Fatalf("write to UDP client: %s", err)
	}

	dg, err := quicClient.ReceiveDatagram(ctx)
	if err != nil {
		t.Fatalf("receive datagram on QUIC client: %s", err)
	}

	if string(dg) != "hello" {
		t.Errorf("unexpected datagram, want: %q, got: %q", "hello", string(dg))
	}

	if err := quicClient.SendDatagram([]byte("hello again")); err != nil {
		t.Fatalf("send datagram on QUIC client: %s", err)
	}

	udpClient.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1500)
	n, err := udpClient.Read(buf)
	if err != nil {
		t.Fatalf("read from UDP client: %s", err)
	}

	if string(buf[:n]) != "hello again" {
		t.Errorf("unexpected UDP data, want: %q, got: %q", "hello again", string(buf[:n]))
	}

	cancel()

	if err := eg.Wait(); err != nil {
		t.Errorf("link error: %s", err)
	}
}

func localUDPPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	c1, c2, err := udp.Pipe()
	if err != nil {
		t.Fatalf("create UDP pipe: %s", err)
	}
	t.Cleanup(func() {
		if err := c1.Close(); err != nil {
			t.Errorf("close c1: %s", err)
		}
		if err := c2.Close(); err != nil {
			t.Errorf("close c2: %s", err)
		}
	})

	return c1, c2
}

func newTLSConfig(t *testing.T, ips ...net.IP) (*tls.Config, *tls.Config) {
	privkey, err := cert.NewPrivateKey()
	if err != nil {
		t.Fatal("create server private key:", err)
	}
	certBytes, err := cert.NewIPCert(ca, caPrivateKey, privkey.Public(), ips...)
	if err != nil {
		t.Fatalf("create IP cert: %s", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	return &tls.Config{
			Certificates: []tls.Certificate{
				{
					Certificate: [][]byte{certBytes},
					PrivateKey:  privkey,
				},
			},
			RootCAs: pool,
		},
		&tls.Config{
			RootCAs: pool,
		}
}

func localQUICPipe(t *testing.T) (*quic.Conn, *quic.Conn) {
	t.Helper()

	quicCfg := &quic.Config{
		EnableDatagrams: true,
	}

	srvUDPConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp conn1: %s", err)
	}
	t.Cleanup(func() {
		if err := srvUDPConn.Close(); err != nil {
			t.Errorf("close server UDP conn: %s", err)
		}
	})

	srvConf, cliConf := newTLSConfig(t, srvUDPConn.LocalAddr().(*net.UDPAddr).IP)

	srv, err := quic.Listen(srvUDPConn, srvConf, quicCfg)
	if err != nil {
		t.Fatal("listen quic:", err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close quic server: %s", err)
		}
	})

	var eg errgroup.Group
	var srvConn *quic.Conn
	eg.Go(func() error {
		var err error
		srvConn, err = srv.Accept(context.TODO())
		return err
	})

	clientUDPConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp conn2: %s", err)
	}
	t.Cleanup(func() {
		if err := clientUDPConn.Close(); err != nil {
			t.Errorf("close client UDP conn: %s", err)
		}
	})

	cliConn, err := quic.Dial(context.TODO(), clientUDPConn, srvUDPConn.LocalAddr(), cliConf, quicCfg)
	if err != nil {
		t.Fatal("dial quic:", err)
	}
	t.Cleanup(func() {
		if err := cliConn.CloseWithError(0, "test done"); err != nil {
			t.Errorf("close quic client: %s", err)
		}
	})

	if err := eg.Wait(); err != nil {
		t.Fatal("accept quic conn:", err)
	}

	return srvConn, cliConn
}
