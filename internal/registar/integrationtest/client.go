package integrationtest

import (
	"context"
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

	public, err := discovery.Bind(conn, server)
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

	client, err := registar.NewClient(context.TODO(), tr, srv.TLSConfig(), srv.URL(), secret, key)
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

func ServeAgent(t *testing.T, agent *control.Agent, stream *http3.RequestStream) {
	t.Helper()

	var eg errgroup.Group
	eg.Go(func() error {
		return agent.Serve(stream)
	})

	t.Cleanup(func() {
		stream.CancelRead(errcode.Cancelled)
		stream.CancelWrite(errcode.Cancelled)

		if err := eg.Wait(); err != nil {
			if !errcode.IsLocalHTTPError(err, errcode.Cancelled) && !errcode.IsLocalStreamError(err, errcode.Cancelled) {
				t.Errorf("serve agent: %s", err)
			}
		}
	})
}
