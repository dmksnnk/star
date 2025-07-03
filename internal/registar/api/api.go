package api

import (
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const pathValuePeerID = "peerID"

// API is a HTTP API for registar service.
type API struct {
	service *registar.Registar
}

// New creates a new API on top of the given service.
func New(s *registar.Registar) API {
	return API{
		service: s,
	}
}

// RegisterGame registers a game on the server.
func (a API) RegisterGame(w http.ResponseWriter, r *http.Request) {
	if !a.validateHTTP3Settings(w) {
		return
	}

	w.WriteHeader(http.StatusOK)

	streamer := w.(http3.HTTPStreamer)
	stream := streamer.HTTPStream()

	key, _ := auth.KeyFromContext(r.Context())
	a.service.RegisterGame(r.Context(), key, stream)
}

// ConnectGame connects a game to the server.
func (a API) ConnectGame(w http.ResponseWriter, r *http.Request) {
	if !a.validateHTTP3Settings(w) {
		return
	}

	key, _ := auth.KeyFromContext(r.Context())
	peerID := r.PathValue(pathValuePeerID)
	if peerID == "" {
		http.Error(w, "missing peer id", http.StatusBadRequest)
		return
	}

	streamer := w.(http3.HTTPStreamer)
	if err := a.service.ConnectGame(r.Context(), key, peerID, streamer); err != nil {
		if errors.Is(err, registar.ErrHostNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		if errors.Is(err, registar.ErrPeerAlreadyConnected) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}

		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// Forward forwards a peer's conn to the game host conn.
func (a API) Forward(w http.ResponseWriter, r *http.Request) {
	if !a.validateHTTP3Settings(w) {
		return
	}

	key, _ := auth.KeyFromContext(r.Context())
	peerID := r.PathValue(pathValuePeerID)
	if peerID == "" {
		http.Error(w, "missing peer id", http.StatusBadRequest)
		return
	}

	streamer := w.(http3.HTTPStreamer)
	if err := a.service.Forward(r.Context(), key, peerID, streamer); err != nil {
		if errors.Is(err, registar.ErrPeerNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (a API) validateHTTP3Settings(w http.ResponseWriter) bool {
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
func NewRouter(api API, secret []byte) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(
		"CONNECT /games/{token}",
		httpplatform.Wrap(
			http.HandlerFunc(api.RegisterGame),
			httpplatform.Authenticate(secret, httpplatform.TokenFromPathValue("token")),
		),
	)
	mux.Handle(
		"CONNECT /games/{token}/connect/{peerID}",
		httpplatform.Wrap(
			http.HandlerFunc(api.ConnectGame),
			httpplatform.Authenticate(secret, httpplatform.TokenFromPathValue("token")),
		),
	)

	mux.Handle(
		"CONNECT /games/{token}/forward/{peerID}",
		httpplatform.Wrap(
			http.HandlerFunc(api.Forward),
			httpplatform.Authenticate(secret, httpplatform.TokenFromPathValue("token")),
		),
	)

	return mux
}
