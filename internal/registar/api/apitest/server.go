package apitest

import (
	"net/url"
	"testing"

	"github.com/dmksnnk/star/internal/platform/http3platform/http3test"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/api"
)

type Server struct {
	srv *http3test.Server
}

// NewServer creates a new test server.
func NewServer(t *testing.T, secret []byte, svc *registar.Registar) *Server {
	t.Helper()

	mux := api.NewRouter(api.New(svc), secret)
	srv := http3test.NewTestServer(t, mux)

	return &Server{
		srv: srv,
	}
}

// Client creates a new client for the server.
func (s *Server) Client(t *testing.T, secret []byte) *api.Client {
	base := &url.URL{
		Scheme: "https",
		Host:   s.srv.Addr().String(),
	}

	dialer := s.srv.Dialer()

	cl := api.NewClient(dialer, base, secret)
	t.Cleanup(func() {
		if err := cl.Close(); err != nil {
			t.Error("close client:", err)
		}
	})

	return cl
}
