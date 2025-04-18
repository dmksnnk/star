package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	http3platform "github.com/dmksnnk/star/internal/platform/http3"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const pathValuePeerID = "peerID"

// API is a HTTP API for registar service.
type API struct {
	service Service
}

// Service is a registar service.
type Service interface {
	// RegisterGame registers a game.
	RegisterGame(ctx context.Context, key auth.Key, stream *http3platform.StreamConn)
	// GameExists returns true if a game with the given key exists.
	GameExists(key auth.Key) bool
	// PeerExists returns true if a peer exists.
	PeerExists(key auth.Key, peerID string) bool
	// ConnectGame connects a peer to a game. Service will handle the conn closing, game host closing and reporting errors, etc.
	ConnectGame(ctx context.Context, key auth.Key, peerID string, stream http3.Stream)
	// WaitingPeerExists returns true if a peer waiting for forwarding exists.
	WaitingPeerExists(key auth.Key, peerID string) bool
	// Forward forwards peer's conn to the game host conn.
	Forward(ctx context.Context, key auth.Key, peerID string, hostConn http3.Stream)
}

// New creates a new API on top of the given service.
func New(s Service) API {
	return API{
		service: s,
	}
}

// RegisterGame registers a game on the server.
func (a API) RegisterGame(w http.ResponseWriter, r *http.Request) {
	conn, ok := a.http3Conn(w)
	if !ok {
		return
	}

	stream, err := conn.OpenStreamSync(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("open stream: %s", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)

	key, _ := auth.KeyFromContext(r.Context())
	streamConn := http3platform.NewStreamConn(stream, conn.LocalAddr(), conn.RemoteAddr())
	a.service.RegisterGame(r.Context(), key, streamConn)
}

// ConnectGame connects a game to the server.
func (a API) ConnectGame(w http.ResponseWriter, r *http.Request) {
	_, ok := a.http3Conn(w)
	if !ok {
		return
	}

	key, _ := auth.KeyFromContext(r.Context())
	if !a.service.GameExists(key) {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	peerID := r.PathValue(pathValuePeerID)
	if peerID == "" {
		http.Error(w, "missing peer id", http.StatusBadRequest)
		return
	}

	if a.service.PeerExists(key, peerID) {
		http.Error(w, "peer already exists", http.StatusConflict)
		return
	}

	w.WriteHeader(http.StatusOK)

	stream := w.(http3.HTTPStreamer).HTTPStream() // this will Flush the response if no response was written

	a.service.ConnectGame(r.Context(), key, peerID, stream)
}

// Forward forwards a peer's conn to the game host conn.
func (a API) Forward(w http.ResponseWriter, r *http.Request) {
	_, ok := a.http3Conn(w)
	if !ok {
		return
	}

	key, _ := auth.KeyFromContext(r.Context())
	if !a.service.GameExists(key) {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	peerID := r.PathValue(pathValuePeerID)
	if peerID == "" {
		http.Error(w, "missing peer id", http.StatusBadRequest)
		return
	}

	if !a.service.WaitingPeerExists(key, peerID) {
		http.Error(w, "peer not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)

	stream := w.(http3.HTTPStreamer).HTTPStream() // this will Flush the response if no response was written

	a.service.Forward(r.Context(), key, peerID, stream)
}

func (a API) http3Conn(w http.ResponseWriter) (http3.Connection, bool) {
	conn := w.(http3.Hijacker).Connection()
	// wait for the client's SETTINGS
	select {
	case <-conn.ReceivedSettings():
	case <-time.After(10 * time.Second):
		// didn't receive SETTINGS within 10 seconds
		http.Error(w, "timeout waiting for client's SETTINGS", http.StatusRequestTimeout)
		return nil, false
	}

	settings := conn.Settings()
	if !settings.EnableDatagrams {
		http.Error(w, "datagrams are not enabled", http.StatusBadRequest)
		return nil, false
	}

	return conn, true
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
