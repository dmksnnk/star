package registartest

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/dmksnnk/star/internal/platform/http3platform/http3test"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

func NewServer(t *testing.T, secret []byte) *Server {
	t.Helper()

	root, err := registar.NewRootCA()
	if err != nil {
		t.Fatalf("create root CA: %s", err)
	}

	srv := registar.NewServer(registar.NewAuthority(root))
	srv.Logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv.H3.Handler = registar.NewRouter(srv, secret)

	conn := NewLocalUDPConn(t)

	ca, tlsConf := http3test.ServerTLSConfig(t, conn.LocalAddr().(*net.UDPAddr).IP)

	srv.H3.TLSConfig = tlsConf

	var eg errgroup.Group
	eg.Go(func() error {
		return srv.Serve(conn)
	})

	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error("shutdown server:", err)
		}

		if err := eg.Wait(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				t.Error("server server:", err)
			}
		}
	})

	return &Server{
		Srv:  srv,
		ca:   ca,
		conn: conn,
	}
}

type Server struct {
	Srv  *registar.Server
	ca   *x509.Certificate
	conn *net.UDPConn
}

func (s *Server) TLSConfig() *tls.Config {
	root := x509.NewCertPool()
	root.AddCert(s.ca)

	return &tls.Config{
		RootCAs:    root,
		NextProtos: []string{http3.NextProtoH3},
	}
}

func (s *Server) URL() *url.URL {
	return &url.URL{
		Scheme: "https",
		Host:   s.conn.LocalAddr().String(),
	}
}

var localhost = net.IPv4(127, 0, 0, 1)

func NewLocalUDPConn(t *testing.T) *net.UDPConn {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: localhost, Port: 0})
	if err != nil {
		t.Fatalf("listen UDP: %s", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close UDP conn: %v", err)
		}
	})

	return conn
}
