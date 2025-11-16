package integrationtest

import (
	"net"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/discovery"
	"github.com/dmksnnk/star/internal/platform/http3platform/http3test"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/relay"
	"golang.org/x/sync/errgroup"
)

func NewServer(t *testing.T, secret []byte, reg registar.Registar) *http3test.Server {
	t.Helper()

	rootCA, err := registar.NewRootCA()
	if err != nil {
		t.Fatalf("NewRootCA: %s", err)
	}

	caAuthority := registar.NewAuthority(rootCA)
	api := registar.NewAPI(reg, caAuthority)
	t.Cleanup(func() {
		if err := api.Close(); err != nil {
			t.Errorf("API Close: %v", err)
		}
	})

	router := registar.NewRouter(api, secret)

	return http3test.NewTestServer(t, router)
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
