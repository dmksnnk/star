package discovery_test

import (
	"net"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/discovery"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
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
	server := integrationtest.ServeDiscovery(t)

	// Client side socket
	clientConn := integrationtest.NewLocalUDPConn(t)

	publicAddr, err := discovery.Bind(clientConn, server)
	if err != nil {
		t.Fatalf("Bind error: %v", err)
	}

	if clientConn.LocalAddr().(*net.UDPAddr).AddrPort().Compare(publicAddr) != 0 {
		t.Fatalf("expected %v, got: %v", clientConn.LocalAddr(), publicAddr)
	}
}
