package registar

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

const (
	headerToken = "Token"
)

type contextKey string

const (
	quicConnContextKey contextKey = "quic-conn"
)

const (
	errCodeAcceptStream = quic.ApplicationErrorCode(0xabcdef)
)

const (
	errCodeNoError               = quic.StreamErrorCode(0x1000)
	errCodeHostAlreadyRegistered = quic.StreamErrorCode(0x1001)
	errCodeHostNotFound          = quic.StreamErrorCode(0x1002)
	errCodeP2PFailed             = quic.StreamErrorCode(0x1003)
)

type Server struct {
	// H3 is the HTTP/3 server used to serve registar requests.
	// Can be additionally configured before serving.
	H3 *http3.Server
	// Logger is used for server error logging.
	// If nil, defaults to slog.Default().
	// This is separate from H3.Logger which handles HTTP/3 internal logging.
	Logger *slog.Logger
	// HostDisconnected specifies optional callback when host disconnects.
	HostDisconnected func(auth.Key, AddrPair)
	// HostConnected specifies optional callback when host connects.
	HostConnected func(auth.Key, AddrPair)
	// PeerConnected specifies optional callback when a peer successfully connects to a host
	// (via P2P or Relay). Calls with the host key and the peer's addrs.
	PeerConnected func(auth.Key, AddrPair)

	ctx       context.Context // is closed when Close is called
	ctxCancel context.CancelFunc
	once      sync.Once

	connsCount sync.WaitGroup
	connsMu    sync.Mutex

	registeredConns map[*quic.Conn]struct{}

	caAuthority *Authority
	hostsMu     sync.RWMutex
	hosts       map[auth.Key]*agent

	relayMu      sync.Mutex
	relayWaiting map[string]waitingSession
	relayCount   sync.WaitGroup
}

type waitingSession struct {
	stream *http3.Stream
	ready  chan struct{} // closed by second arrival once proxy is running
}

// NewServer creates a new registar server.
// It configures the server to use the provided registar backend.
func NewServer(ca *Authority) *Server {
	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		H3:              &http3.Server{},
		ctx:             ctx,
		ctxCancel:       cancel,
		registeredConns: make(map[*quic.Conn]struct{}),
		caAuthority:     ca,
		hosts:           make(map[auth.Key]*agent),
		relayWaiting:    make(map[string]waitingSession),
	}
}

func (s *Server) Register(w http.ResponseWriter, r *http.Request, key auth.Key, isHost bool) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	caCert, cert, err := s.caAuthority.NewSessionCert(key, (*x509.CertificateRequest)(req.CSR))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := RegisterResponse{
		CACert: (*Certificate)(caCert),
		Cert:   (*Certificate)(cert),
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	conn, ok := r.Context().Value(quicConnContextKey).(*quic.Conn)
	if !ok {
		http.Error(w, "no QUIC connection in context", http.StatusInternalServerError)
		return
	}

	s.registerConn(conn)

	addrs := AddrPair{
		Public:  conn.RemoteAddr().(*net.UDPAddr).AddrPort(),
		Private: req.PrivateAddr,
	}

	// overtake the stream
	rc := http3platform.NewResponseController(w)
	str, err := rc.HTTPStream()
	if err != nil {
		s.Logger.Error("overtake HTTP3 stream", "err", err)
		str.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
		str.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
		return
	}

	if isHost {
		s.registerHost(str, key, addrs)
		return
	}

	s.connectPeer(str, key, addrs)
}

func (s *Server) registerConn(conn *quic.Conn) {
	s.connsMu.Lock()
	s.registeredConns[conn] = struct{}{}
	s.connsMu.Unlock()

	context.AfterFunc(conn.Context(), func() {
		s.connsMu.Lock()
		delete(s.registeredConns, conn)
		s.connsMu.Unlock()
	})
}

func (s *Server) registerHost(str *http3.Stream, key auth.Key, addrs AddrPair) {
	s.hostsMu.Lock()
	if _, ok := s.hosts[key]; ok {
		s.hostsMu.Unlock()
		s.Logger.Error("host already registered", "key", key)
		str.CancelRead(errCodeHostAlreadyRegistered)
		str.CancelWrite(errCodeHostAlreadyRegistered)
		return
	}

	s.hosts[key] = newAgent(str, addrs)
	s.hostsMu.Unlock()

	context.AfterFunc(str.Context(), func() {
		s.hostsMu.Lock()
		defer s.hostsMu.Unlock()

		delete(s.hosts, key)
		s.caAuthority.RemoveSessionCA(key)
		if s.HostDisconnected != nil {
			s.HostDisconnected(key, addrs)
		}
	})

	if s.HostConnected != nil {
		s.HostConnected(key, addrs)
	}
}

func (s *Server) connectPeer(str *http3.Stream, key auth.Key, addrs AddrPair) {
	s.hostsMu.RLock()
	host, ok := s.hosts[key]
	s.hostsMu.RUnlock()
	if !ok {
		s.Logger.Error("host not found", "key", key)
		str.CancelRead(errCodeHostNotFound)
		str.CancelWrite(errCodeHostNotFound)
		return
	}

	errCode := errCodeNoError
	defer func() {
		str.CancelRead(errCode)
		str.CancelWrite(errCode)
	}()

	// lock the host during setup to avoid concurrent connection attempts
	host.mu.Lock()
	defer host.mu.Unlock()

	// overtake the stream
	peer := &agent{
		str:   str,
		addrs: addrs,
	}
	err := initP2P(host, peer)
	if err == nil {
		if s.PeerConnected != nil {
			s.PeerConnected(key, addrs)
		}
		return
	}

	if !errors.Is(err, control.ErrConnectFailed) {
		s.Logger.Error("init P2P connection", "err", err)
		errCode = errCodeP2PFailed
		return
	}

	if err := initRelay(host.str, peer.str, newSessionID()); err != nil {
		s.Logger.Error("init relay connection", "err", err)
		errCode = errCodeP2PFailed
		return
	}

	if s.PeerConnected != nil {
		s.PeerConnected(key, addrs)
	}
}

func (s *Server) Relay(w http.ResponseWriter, r *http.Request, sessionID string) {
	conn, ok := r.Context().Value(quicConnContextKey).(*quic.Conn)
	if !ok {
		http.Error(w, "no QUIC connection in context", http.StatusInternalServerError)
		return
	}

	s.connsMu.Lock()
	_, ok = s.registeredConns[conn]
	s.connsMu.Unlock()
	if !ok {
		http.Error(w, "unregistered connection", http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusOK)
	rc := http3platform.NewResponseController(w)
	str, err := rc.HTTPStream()
	if err != nil {
		s.Logger.Error("overtake HTTP3 stream", "err", err)
		str.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
		str.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestRejected))
		return
	}

	s.relayMu.Lock()
	if waiting, ok := s.relayWaiting[sessionID]; ok {
		delete(s.relayWaiting, sessionID)
		s.relayMu.Unlock()

		defer close(waiting.ready) // let the waiting side know that the proxy is ready

		s.relayCount.Add(1)
		go func() {
			defer s.relayCount.Done()

			if err := relay(s.ctx, waiting.stream, str); err != nil {
				if errors.Is(err, context.Canceled) && s.ctx.Err() != nil {
					// server is closing, ignore
					return
				}
				s.Logger.Error("relay", "err", err)
			}
		}()

		return
	}

	waiting := waitingSession{
		stream: str,
		ready:  make(chan struct{}),
	}

	s.relayWaiting[sessionID] = waiting
	s.relayMu.Unlock()

	select {
	case <-str.Context().Done():
		s.relayMu.Lock()
		delete(s.relayWaiting, sessionID)
		s.relayMu.Unlock()
	case <-waiting.ready:
		return
	}
}

func relay(ctx context.Context, a, b *http3.Stream) error {
	eg, ctx := errgroup.WithContext(ctx) // depend on server context

	eg.Go(func() error {
		for {
			dg, err := a.ReceiveDatagram(ctx)
			if err != nil {
				return fmt.Errorf("read from stream: %w", err)
			}

			if err := b.SendDatagram(dg); err != nil {
				return fmt.Errorf("send datagram: %w", err)
			}
		}
	})

	eg.Go(func() error {
		for {
			dg, err := b.ReceiveDatagram(ctx)
			if err != nil {
				return fmt.Errorf("read from stream: %w", err)
			}

			if err := a.SendDatagram(dg); err != nil {
				return fmt.Errorf("send datagram: %w", err)
			}
		}
	})

	return eg.Wait()
}

func (s *Server) init() {
	s.once.Do(func() {
		origConnContext := s.H3.ConnContext
		s.H3.ConnContext = func(ctx context.Context, c *quic.Conn) context.Context {
			if origConnContext != nil {
				ctx = origConnContext(ctx, c)
			}
			return context.WithValue(ctx, quicConnContextKey, c)
		}
		if s.Logger == nil {
			s.Logger = slog.Default()
		}
		s.H3.EnableDatagrams = true
	})
}

func (s *Server) Serve(conn net.PacketConn) error {
	var quicConf *quic.Config
	if s.H3.QUICConfig != nil {
		quicConf = s.H3.QUICConfig.Clone()
	} else {
		quicConf = &quic.Config{
			EnableDatagrams: true,
		}
	}

	ln, err := quic.ListenEarly(conn, s.H3.TLSConfig, quicConf)
	if err != nil {
		return err
	}
	defer ln.Close()

	for {
		qconn, err := ln.Accept(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil { // server closed
				return http.ErrServerClosed
			}
			return err
		}
		s.connsCount.Add(1)

		go func() {
			defer s.connsCount.Done()

			if err := s.ServeQUICConn(qconn); err != nil {
				s.Logger.ErrorContext(qconn.Context(), "serve http3 connection", "err", err)
			}
		}()
	}
}

func (s *Server) ServeQUICConn(conn *quic.Conn) error { // TODO: probably just use H3 server to server, no need for custom
	s.init()

	http3Conn, err := s.H3.NewRawServerConn(conn)
	if err != nil {
		return err
	}

	eg, ctx := errgroup.WithContext(s.ctx)
	eg.Go(func() error {
		// Accept connection until server is closed.
		// If connection gets overtaken, it will only open new streams,
		// so it is OK to keep accepting for other streams.
		for {
			str, err := conn.AcceptStream(ctx)
			if err != nil { // server context closed or error
				if isRemoteClientClosed(err) {
					return nil
				}

				return fmt.Errorf("accept stream: %w", err)
			}

			eg.Go(func() error {
				http3Conn.HandleRequestStream(str)
				return nil
			})
		}
	})

	eg.Go(func() error {
		for {
			str, err := conn.AcceptUniStream(ctx) // HTTP/3 control streams
			if err != nil {
				if isRemoteClientClosed(err) {
					return nil
				}

				return fmt.Errorf("accept unidirectional stream: %w", err)
			}

			eg.Go(func() error {
				http3Conn.HandleUnidirectionalStream(str)
				return nil
			})
		}
	})

	if err := eg.Wait(); err != nil {
		http3Conn.CloseWithError(errCodeAcceptStream, err.Error())
		return err
	}

	http3Conn.CloseWithError(0, "")

	return nil
}

func (s *Server) ListenAndServe() error {
	addr := s.H3.Addr
	if addr == "" {
		addr = ":https"
	}
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	return s.Serve(conn)
}

func (s *Server) ListenAndServeTLS(certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}

	if s.H3.TLSConfig == nil {
		s.H3.TLSConfig = &tls.Config{}
	}
	s.H3.TLSConfig.Certificates = append(s.H3.TLSConfig.Certificates, cert)

	return s.ListenAndServe()
}

func (s *Server) Close() error {
	s.ctxCancel()

	s.hostsMu.Lock()
	for key := range s.hosts {
		delete(s.hosts, key)
		s.caAuthority.RemoveSessionCA(key)
	}
	s.hostsMu.Unlock()

	err := s.H3.Close()
	s.relayCount.Wait()
	s.connsCount.Wait()

	return err
}

// NewRouter creates a new HTTP handler for the registar server.
func NewRouter(srv *Server, secret []byte) *http.ServeMux {
	hostHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _ := auth.KeyFromContext(r.Context())

		srv.Register(w, r, key, true)
	})
	joinHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _ := auth.KeyFromContext(r.Context())

		srv.Register(w, r, key, false)
	})
	relayHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.PathValue("sessionID")
		srv.Relay(w, r, sessionID)
	})

	authMW := httpplatform.Authenticate(secret, httpplatform.TokenFromHeader(headerToken))

	mux := http.NewServeMux()
	mux.Handle(
		"POST /host",
		httpplatform.Wrap(hostHandler, authMW),
	)
	mux.Handle(
		"POST /join",
		httpplatform.Wrap(joinHandler, authMW),
	)
	mux.Handle(
		"POST /relay/{sessionID}",
		httpplatform.Wrap(relayHandler, authMW),
	)

	return mux
}

func isRemoteClientClosed(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) &&
		appErr.ErrorCode == errCodeClientClosed &&
		appErr.Remote
}

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
