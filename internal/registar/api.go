package registar

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"time"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	headerToken = "Token"
)

// API is a HTTP API for registar service.
type API struct {
	registar    Registar
	caAuthority *Authority
}

type Registar interface {
	Host(ctx context.Context, key auth.Key, streamer http3.HTTPStreamer, addrs AddrPair) error
	Join(ctx context.Context, key auth.Key, streamer http3.HTTPStreamer, addrs AddrPair) error
}

// NewAPI creates a new API on top of the given service.
func NewAPI(r Registar, caAuthority *Authority) *API {
	return &API{
		registar:    r,
		caAuthority: caAuthority,
	}
}

func (a *API) SessionCert(w http.ResponseWriter, r *http.Request) {
	var req CertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	key, _ := auth.KeyFromContext(r.Context())

	// TODO: move to Registar, remove session CA on session end
	caCert, cert, err := a.caAuthority.NewSessionCert(key, (*x509.CertificateRequest)(req.CSR))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := CertResponse{
		CACert: (*Certificate)(caCert),
		Cert:   (*Certificate)(cert),
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (a *API) Host(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := req.UnmarshalHTTP(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	addrs := AddrPair{
		Public:  req.PublicAddr,
		Private: req.PrivateAddr,
	}

	key, _ := auth.KeyFromContext(r.Context())

	streamer := w.(http3.HTTPStreamer)
	if err := a.registar.Host(r.Context(), key, streamer, addrs); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
}

func (a *API) Join(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := req.UnmarshalHTTP(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	addrs := AddrPair{
		Public:  req.PublicAddr,
		Private: req.PrivateAddr,
	}

	key, _ := auth.KeyFromContext(r.Context())

	streamer := w.(http3.HTTPStreamer)
	if err := a.registar.Join(r.Context(), key, streamer, addrs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (a *API) validateHTTP3Settings(w http.ResponseWriter) bool {
	conn := w.(http3.Hijacker).Connection()
	// wait for the client's SETTINGS
	select {
	case <-conn.ReceivedSettings():
	case <-time.After(10 * time.Second):
		// didn't receive SETTINGS within 10 seconds
		http.Error(w, "timeout waiting for client's SETTINGS", http.StatusRequestTimeout)
		return false
	}

	settings := conn.Settings()
	if !settings.EnableDatagrams {
		http.Error(w, "datagrams are not enabled", http.StatusBadRequest)
		return false
	}

	return true
}

// NewServer creates a new HTTP/3 server.
func NewServer(addr string, handler http.Handler, tlsConf *tls.Config) *http3.Server {
	return &http3.Server{
		Addr:            addr,
		Handler:         handler,
		EnableDatagrams: true,
		TLSConfig:       tlsConf,
		QUICConfig: &quic.Config{
			KeepAlivePeriod: 10 * time.Second,
			Allow0RTT:       true,
			// DisablePathMTUDiscovery: true,
		},
	}
}

// NewRouter return HTTP multiplexer with API routes.
func NewRouter(api *API, secret []byte) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(
		"POST /session-cert",
		httpplatform.Wrap(
			http.HandlerFunc(api.SessionCert),
			httpplatform.Authenticate(secret, httpplatform.TokenFromHeader(headerToken)),
		),
	)
	mux.Handle(
		"POST /host",
		httpplatform.Wrap(
			http.HandlerFunc(api.Host),
			httpplatform.Authenticate(secret, httpplatform.TokenFromHeader(headerToken)),
		),
	)
	mux.Handle(
		"POST /join",
		httpplatform.Wrap(
			http.HandlerFunc(api.Join),
			httpplatform.Authenticate(secret, httpplatform.TokenFromHeader(headerToken)),
		),
	)

	return mux
}
