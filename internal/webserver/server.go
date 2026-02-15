package webserver

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/dmksnnk/star/internal/forwarder"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/web"
)

// GameEntry represents a known game with its default port.
type GameEntry struct {
	Name string
	Port int
}

// DefaultGames is the list of known games shown in the host form dropdown.
var DefaultGames = []GameEntry{
	{Name: "Stardew Valley", Port: 24642},
	{Name: "Terraria", Port: 7777},
	{Name: "Factorio", Port: 34197},
	{Name: "Minecraft (Bedrock)", Port: 19132},
	{Name: "Valheim", Port: 2456},
}

// Server is the web UI HTTP server.
type Server struct {
	templates  map[string]*template.Template
	logHandler *SSEHandler

	mu      sync.Mutex
	session *activeSession
	wg      sync.WaitGroup

	sharedConfig sharedConfig
}

// New creates a new Server and registers all routes.
func New() (*Server, error) {
	templates, err := parseTemplates(web.Templates)
	if err != nil {
		return nil, err
	}

	sseHandler := NewSSEHandler(100)

	s := &Server{
		templates:  templates,
		logHandler: sseHandler,
	}
	return s, nil
}

// Stop gracefully stops any active session and waits for
// all background goroutines to finish.
func (s *Server) Stop() {
	s.mu.Lock()
	sess := s.session
	if sess != nil {
		s.session = nil
	}
	s.mu.Unlock()

	if sess != nil {
		sess.stop()
		s.logHandler.Reset()
	}

	s.wg.Wait()
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := s.templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if s.redirectIfSession(w, r) {
		return
	}

	s.render(w, "index.html", indexData{})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.redirectIfSession(w, r) {
		return
	}

	var cfg sharedConfig
	if errs := cfg.UnmarshalHTTP(r); len(errs) > 0 {
		s.render(w, "index.html", indexData{Errors: errs, Values: r.Form})
		return
	}

	s.mu.Lock()
	s.sharedConfig = cfg
	s.mu.Unlock()

	mode := r.FormValue("mode")
	switch mode {
	case "host":
		http.Redirect(w, r, routeHost, http.StatusSeeOther)
	case "peer":
		http.Redirect(w, r, routePeer, http.StatusSeeOther)
	default:
		http.Redirect(w, r, routeIndex, http.StatusSeeOther)
	}
}

func (s *Server) handleHostForm(w http.ResponseWriter, r *http.Request) {
	if s.redirectIfSession(w, r) {
		return
	}
	s.render(w, "host.html", hostFormData{
		Games: DefaultGames,
	})
}

func (s *Server) handleHostStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.session != nil {
		s.redirectToSession(w, r)
		return
	}

	var cfg hostConfig
	if errs := cfg.UnmarshalHTTP(r); len(errs) > 0 {
		s.render(w, "host.html", hostFormData{
			Games:  DefaultGames,
			Errors: errs,
			Values: r.Form,
		})
		return
	}

	token := auth.NewToken(cfg.GameKey, []byte(s.sharedConfig.Secret))
	logger := slog.New(s.logHandler)
	fwdCfg := forwarder.HostConfig{
		Logger:     logger,
		TLSConfig:  s.sharedConfig.tlsConfig(),
		ListenAddr: &net.UDPAddr{Port: s.sharedConfig.Port},
		ErrHandlers: []func(error){
			func(err error) {
				logger.Error("host forwarder error", "error", err)
			},
		},
	}

	host, err := fwdCfg.Register(r.Context(), s.sharedConfig.RegistarURL, token)
	if err != nil {
		s.render(w, "host.html", hostFormData{
			Games:  DefaultGames,
			Errors: []error{fmt.Errorf("register host: %w", err)},
			Values: r.Form,
		})
		return
	}

	sess := &activeSession{
		kind: "host",
		done: make(chan struct{}),
		info: sessionInfo{
			InviteCode:  cfg.GameKey,
			GameAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.GamePort)),
		},
	}
	s.session = sess

	logger.Info("game registered", "key", cfg.GameKey.String())

	gameAddr := &net.UDPAddr{Port: cfg.GamePort}
	ctx, cancel := context.WithCancel(context.Background())

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := host.AcceptAndLink(ctx, gameAddr); err != nil {
			logger.Error("run host", "error", err)
		}
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		<-sess.done
		cancel()
		if err := host.Close(); err != nil {
			logger.Error("close host", "error", err)
		}
	}()

	http.Redirect(w, r, routeHostRunning, http.StatusSeeOther)
}

func (s *Server) handleHostRunning(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()

	if sess == nil || sess.kind != "host" {
		http.Redirect(w, r, routeIndex, http.StatusSeeOther)
		return
	}

	s.render(w, "host-running.html", hostRunningData{
		InviteCode:  sess.info.InviteCode,
		GameAddress: sess.info.GameAddress,
	})
}

func (s *Server) handleHostStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sess := s.session
	if sess != nil {
		s.session = nil
	}
	s.mu.Unlock()

	if sess != nil {
		sess.stop()
		s.logHandler.Reset()
	}

	http.Redirect(w, r, routeIndex, http.StatusSeeOther)
}

func (s *Server) handlePeerForm(w http.ResponseWriter, r *http.Request) {
	if s.redirectIfSession(w, r) {
		return
	}
	s.render(w, "peer.html", peerFormData{})
}

func (s *Server) handlePeerStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.session != nil {
		s.redirectToSession(w, r)
		return
	}

	var cfg peerConfig
	if errs := cfg.UnmarshalHTTP(r); len(errs) > 0 {
		s.render(w, "peer.html", peerFormData{
			Errors: errs,
			Values: r.Form,
		})
		return
	}

	logger := slog.New(s.logHandler)
	fwdCfg := forwarder.PeerConfig{
		Logger:             logger,
		TLSConfig:          s.sharedConfig.tlsConfig(),
		RegistarListenAddr: &net.UDPAddr{Port: s.sharedConfig.Port},
		GameListenPort:     cfg.GameListenPort,
	}

	token := auth.NewToken(cfg.InviteCode, []byte(s.sharedConfig.Secret))
	peer, err := fwdCfg.Join(r.Context(), s.sharedConfig.RegistarURL, token)
	if err != nil {
		s.render(w, "peer.html", peerFormData{
			Errors: []error{fmt.Errorf("register peer: %w", err)},
			Values: r.Form,
		})
		return
	}

	sess := &activeSession{
		kind: "peer",
		done: make(chan struct{}),
		info: sessionInfo{
			ListenAddress: peer.UDPAddr(),
		},
	}
	s.session = sess

	logger.Info("peer listening", "addr", peer.UDPAddr().String())

	ctx, cancel := context.WithCancel(context.Background())
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := peer.AcceptAndLink(ctx); err != nil {
			logger.Error("accept and link", "error", err)
		}
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		<-sess.done
		cancel()
		if err := peer.Close(); err != nil {
			logger.Error("close peer", "error", err)
		}
	}()

	http.Redirect(w, r, routePeerConnected, http.StatusSeeOther)
}

func (s *Server) handlePeerConnected(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()

	if sess == nil || sess.kind != "peer" {
		http.Redirect(w, r, routeIndex, http.StatusSeeOther)
		return
	}

	s.render(w, "peer-connected.html", peerConnectedData{
		ListenAddress: sess.info.ListenAddress.String(),
	})
}

func (s *Server) handlePeerStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sess := s.session
	if sess != nil {
		s.session = nil
	}
	s.mu.Unlock()

	if sess != nil {
		sess.stop()
		s.logHandler.Reset()
	}

	http.Redirect(w, r, routeIndex, http.StatusSeeOther)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	history, ch := s.logHandler.Subscribe()
	defer s.logHandler.Unsubscribe(ch)

	// Replay buffered history.
	for _, line := range history {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	flusher.Flush()

	// Stream new lines until client disconnects.
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// redirectIfSession redirects to the active session's page if one exists.
// Returns true if a redirect was sent.
func (s *Server) redirectIfSession(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()

	if sess == nil {
		return false
	}

	s.redirectToSession(w, r)
	return true
}

// redirectToSession redirects to the active session's page.
// Caller must ensure s.session is not nil.
func (s *Server) redirectToSession(w http.ResponseWriter, r *http.Request) {
	switch s.session.kind {
	case "host":
		http.Redirect(w, r, routeHostRunning, http.StatusSeeOther)
	case "peer":
		http.Redirect(w, r, routePeerConnected, http.StatusSeeOther)
	}
}

func parseTemplates(fsys fs.FS) (map[string]*template.Template, error) {
	base, err := template.ParseFS(fsys, "templates/base.html")
	if err != nil {
		return nil, err
	}

	pages := []string{
		"index.html",
		"host.html",
		"host-running.html",
		"peer.html",
		"peer-connected.html",
		"error.html",
	}

	templates := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.Must(base.Clone()).ParseFS(fsys, filepath.Join("templates", page))
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", page, err)
		}

		templates[page] = t
	}

	return templates, nil
}
