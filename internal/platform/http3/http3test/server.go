package http3test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/platform"
	http3platform "github.com/dmksnnk/star/internal/platform/http3"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

type Server struct {
	ca   *x509.Certificate
	conn net.PacketConn
}

// NewTestServer creates a new test server with the given handler.
// It has a self-signed CA and a server certificate.
// It closes itself on test cleanup.
func NewTestServer(t *testing.T, handler http.Handler) *Server {
	ca, caPrivateKey, err := cert.NewCA()
	if err != nil {
		t.Fatal("create CA:", err)
	}

	srvPrivkey, err := cert.NewPrivateKey()
	if err != nil {
		t.Fatal("create server private key:", err)
	}
	srvCert, err := cert.NewIPCert(ca, caPrivateKey, net.IPv4(127, 0, 0, 1), srvPrivkey.Public())
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

	srv := &http3.Server{
		Handler:         handler,
		EnableDatagrams: true,
		TLSConfig:       serverTLSConf,
		QUICConfig: &quic.Config{
			KeepAlivePeriod: 10 * time.Second,
			Allow0RTT:       true,
		},
	}

	conn := newLocalUDPConn(t)
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error("close UDP conn:", err)
		}
	})

	var eg errgroup.Group
	eg.Go(func() error {
		return srv.Serve(conn)
	})

	t.Cleanup(func() {
		if err := srv.Shutdown(context.TODO()); err != nil {
			t.Error("shutdown server:", err)
		}

		if err := eg.Wait(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				t.Error("server:", err)
			}
		}
	})

	return &Server{
		ca:   ca,
		conn: conn,
	}
}

// Dialer returns a new HTTP/3 dialer configure to with the server's CA.
func (s *Server) Dialer() *http3platform.HTTP3Dialer {
	root := x509.NewCertPool()
	root.AddCert(s.ca)
	clientTLSConf := &tls.Config{
		RootCAs:    root,
		NextProtos: []string{http3.NextProtoH3},
	}

	return &http3platform.HTTP3Dialer{
		TLSConfig: clientTLSConf,
		QUICConfig: &quic.Config{
			EnableDatagrams: true,
		},
		EnableExtendedConnect: true,
	}
}

// Addr returns the server's address.
func (s *Server) Addr() net.Addr {
	return s.conn.LocalAddr()
}

func newLocalUDPConn(t *testing.T) net.PacketConn {
	ll := &platform.LocalListener{}
	conn, err := ll.ListenUDP(context.TODO(), 0)
	if err != nil {
		t.Fatal("listen UDP:", err)
	}

	return conn
}
