package integrationtest

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"testing"

	"github.com/dmksnnk/star/internal/discovery"
	"github.com/dmksnnk/star/internal/platform/http3platform/http3test"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/relay"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

func NewServer(t *testing.T, reg registar.Registar, secret []byte) *Server {
	t.Helper()

	srv := registar.NewServer(reg)
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
		ca:   ca,
		conn: conn,
	}
}

type Server struct {
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

func ServeDiscovery(t *testing.T) netip.AddrPort {
	conn := NewLocalUDPConn(t)

	d := discovery.NewServer()

	var eg errgroup.Group
	eg.Go(func() error {
		return d.Serve(conn)
	})

	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("close discovery server: %v", err)
		}

		if err := eg.Wait(); err != nil {
			t.Fatalf("serve discovery: %s", err)
		}
	})

	return conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

func ServeRelay(t *testing.T, ops ...relay.Option) (*relay.UDPRelay, netip.AddrPort) {
	conn := NewLocalUDPConn(t)

	r := relay.NewUDPRelay(ops...)

	var eg errgroup.Group
	eg.Go(func() error {
		return r.Serve(conn)
	})

	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close relay: %v", err)
		}

		if err := eg.Wait(); err != nil {
			t.Errorf("serve relay: %s", err)
		}
	})

	return r, conn.LocalAddr().(*net.UDPAddr).AddrPort()
}
