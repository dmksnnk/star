package webserver

import (
	"net/http"

	"github.com/dmksnnk/star/web"
)

const (
	routeIndex         = "/"
	routeSetup         = "/setup"
	routeHost          = "/host"
	routeHostRunning   = "/host/running"
	routeHostStop      = "/host/stop"
	routePeer          = "/peer"
	routePeerConnected = "/peer/connected"
	routePeerStop      = "/peer/stop"
	routeLogs          = "/logs"
)

// NewRouter creates an [http.ServeMux] with all web UI routes registered.
func NewRouter(s *Server) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", http.StripPrefix("/static/", web.StaticHandler()))

	mux.HandleFunc("GET "+routeIndex+"{$}", s.handleIndex)
	mux.HandleFunc("POST "+routeSetup, s.handleSetup)
	mux.HandleFunc("GET "+routeHost, s.handleHostForm)
	mux.HandleFunc("POST "+routeHost, s.handleHostStart)
	mux.HandleFunc("GET "+routeHostRunning, s.handleHostRunning)
	mux.HandleFunc("POST "+routeHostStop, s.handleHostStop)
	mux.HandleFunc("GET "+routePeer, s.handlePeerForm)
	mux.HandleFunc("POST "+routePeer, s.handlePeerStart)
	mux.HandleFunc("GET "+routePeerConnected, s.handlePeerConnected)
	mux.HandleFunc("POST "+routePeerStop, s.handlePeerStop)
	mux.HandleFunc("GET "+routeLogs, s.handleLogs)

	return mux
}
