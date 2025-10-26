package discovery_test

import (
	"errors"
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
	localUDPAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}

	serverConn, err := net.ListenUDP("udp", localUDPAddr)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}

	var eg errgroup.Group
	eg.Go(func() error {
		return discovery.Serve(serverConn)
	})
	t.Cleanup(func() {
		serverConn.Close()
		if err := eg.Wait(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve error: %v", err)
		}
	})

	// Client side socket
	clientConn, err := net.ListenUDP("udp", localUDPAddr)
	if err != nil {
		t.Fatalf("client ListenPacket: %v", err)
	}
	t.Cleanup(func() {
		clientConn.Close()
	})

	server := serverConn.LocalAddr().(*net.UDPAddr).AddrPort()
	publicAddr, err := discovery.Bind(clientConn, server)
	if err != nil {
		t.Fatalf("Bind error: %v", err)
	}

	if clientConn.LocalAddr().(*net.UDPAddr).AddrPort().Compare(publicAddr) != 0 {
		t.Fatalf("expected %v, got: %v", clientConn.LocalAddr(), publicAddr)
	}
}
