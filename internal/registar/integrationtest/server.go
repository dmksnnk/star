package integrationtest

import (
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/discovery"
	"github.com/dmksnnk/star/internal/platform/http3platform/http3test"
	"github.com/dmksnnk/star/internal/registar"
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

func RunDiscovery(t *testing.T) netip.AddrPort {
	conn := NewLocalUDPConn(t)

	var eg errgroup.Group
	eg.Go(func() error {
		return discovery.Serve(conn)
	})

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error("close discovery conn:", err)
		}

		if err := eg.Wait(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve error: %v", err)
		}
	})

	return conn.LocalAddr().(*net.UDPAddr).AddrPort()
}
