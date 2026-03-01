package discovery_test

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/discovery"
	"golang.org/x/sync/errgroup"
)

func TestRequestMarshalUnmarshal(t *testing.T) {
	req, err := discovery.NewRequest()
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	data, err := req.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	var req2 discovery.Request
	if err := req2.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}

	if req != req2 {
		t.Fatalf("expected %+v, got %+v", req, req2)
	}
}

func TestResponseMarshalUnmarshal(t *testing.T) {
	res := discovery.Response{
		TxID: [16]byte{},
		Addr: netip.MustParseAddrPort("192.0.2.1:1234"),
	}

	data, err := res.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	var res2 discovery.Response
	if err := res2.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}

	if res != res2 {
		t.Fatalf("expected %+v, got %+v", res, res2)
	}
}

func TestBindLoopback(t *testing.T) {
	server := serveDiscovery(t)

	// Client side socket
	clientConn := newLocalUDPConn(t)

	publicAddr, err := discovery.Bind(context.Background(), clientConn, server)
	if err != nil {
		t.Fatalf("Bind error: %v", err)
	}

	if clientConn.LocalAddr().(*net.UDPAddr).AddrPort().Compare(publicAddr) != 0 {
		t.Fatalf("expected %v, got: %v", clientConn.LocalAddr(), publicAddr)
	}
}

func serveDiscovery(t *testing.T) netip.AddrPort {
	conn := newLocalUDPConn(t)

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

func newLocalUDPConn(t *testing.T) *net.UDPConn {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
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
