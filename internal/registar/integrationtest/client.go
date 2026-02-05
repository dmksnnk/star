package integrationtest

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/discovery"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

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
