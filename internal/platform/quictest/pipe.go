package quictest

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"

	"github.com/dmksnnk/star/internal/cert"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

// Pipe creates a pair of connected QUIC connections for testing.
func Pipe(t *testing.T) (client *quic.Conn, server *quic.Conn) {
	t.Helper()

	ca, caPrivateKey, err := cert.NewCA()
	if err != nil {
		t.Fatal("create CA:", err)
	}

	srvPrivkey, err := cert.NewPrivateKey()
	if err != nil {
		t.Fatal("create server private key:", err)
	}
	srvCert, err := cert.NewIPCert(ca, caPrivateKey, srvPrivkey.Public(), net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal("create server cert:", err)
	}

	serverTLSConf := &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{srvCert},
				PrivateKey:  srvPrivkey,
			},
		},
		NextProtos: []string{http3.NextProtoH3},
	}

	root := x509.NewCertPool()
	root.AddCert(ca)
	clientTLSConf := &tls.Config{
		RootCAs:    root,
		NextProtos: []string{http3.NextProtoH3},
	}

	srv, err := quic.Listen(
		newLocalUDPConn(t),
		serverTLSConf,
		&quic.Config{
			EnableDatagrams: true,
		},
	)
	if err != nil {
		t.Fatal("listen quic:", err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close QUIC listener: %s", err)
		}
	})

	var srvConn *quic.Conn
	var eg errgroup.Group
	eg.Go(func() error {
		var err error
		srvConn, err = srv.Accept(t.Context())
		return err
	})

	clConn, err := quic.Dial(
		t.Context(),
		newLocalUDPConn(t),
		srv.Addr(),
		clientTLSConf,
		&quic.Config{
			EnableDatagrams: true,
		},
	)
	if err != nil {
		t.Fatal("dial quic:", err)
	}

	if err := eg.Wait(); err != nil {
		t.Fatal("accept quic:", err)
	}

	return clConn, srvConn
}

func newLocalUDPConn(t *testing.T) *net.UDPConn {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal("listen UDP:", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close local UDP conn: %s", err)
		}
	})

	return conn
}
