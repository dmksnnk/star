package integrationtest

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/discovery"
	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/platform/http3platform/http3test"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

var Localhost = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}

func NewLocalUDPConn(t *testing.T) *net.UDPConn {
	t.Helper()

	conn, err := net.ListenUDP("udp", Localhost)
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

func NewLocalQUICTransport(t *testing.T) *quic.Transport {
	t.Helper()

	conn := NewLocalUDPConn(t)

	tr := &quic.Transport{
		Conn: conn,
	}
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Errorf("close quic transport: %v", err)
		}
	})

	return tr
}

func Discover(t *testing.T, conn net.PacketConn, server netip.AddrPort) registar.AddrPair {
	t.Helper()

	public, err := discovery.Bind(context.Background(), conn, server)
	if err != nil {
		t.Fatalf("discover addrs: %s", err)
	}

	return registar.AddrPair{
		Public:  public,
		Private: conn.LocalAddr().(*net.UDPAddr).AddrPort(),
	}
}

func NewClient(t *testing.T, tr *quic.Transport, srv *http3test.Server, secret []byte, key auth.Key) *registar.RegisteredClient {
	t.Helper()

	token := auth.NewToken(key, secret)

	client, err := registar.RegisterClient(context.TODO(), tr, srv.TLSConfig(), srv.URL(), token)
	if err != nil {
		t.Fatalf("register client: %s", err)
	}

	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})

	return client
}

func ServeAgent(t *testing.T, agent *control.Agent, conn *quic.Conn) {
	var eg errgroup.Group
	eg.Go(func() error {
		return agent.Serve(conn)
	})

	t.Cleanup(func() {
		conn.CloseWithError(0, "test cleanup")
		if err := eg.Wait(); err != nil {
			if !isLocalClose(err) {
				t.Errorf("serve agent: %s", err)
			}
		}
	})
}

func isLocalClose(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) &&
		appErr.ErrorCode == 0 &&
		!appErr.Remote &&
		appErr.ErrorMessage == "test cleanup"
}
