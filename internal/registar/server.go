package registar

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"

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

var (
	errCodeAcceptStream = quic.ApplicationErrorCode(0xabcdef)
	errCodePeerClosed   = quic.ApplicationErrorCode(0xabcdf0)
)

type Registar interface {
	NewSessionCert(key auth.Key, csr *x509.CertificateRequest) (caCert, cert *x509.Certificate, err error)
	Host(ctx context.Context, key auth.Key, peer Peer) error
	Join(ctx context.Context, key auth.Key, peer Peer) error
	Close() error
}

type Server struct {
	// H3 is the HTTP/3 server used to serve registar requests.
	// Can be additionally configured before serving.
	H3 *http3.Server
	// Logger is used for server error logging.
	// If nil, defaults to slog.Default().
	// This is separate from H3.Logger which handles HTTP/3 internal logging.
	Logger *slog.Logger

	registar   Registar
	ctx        context.Context // is closed when Close is called
	ctxCancel  context.CancelFunc
	connsCount sync.WaitGroup
	once       sync.Once
}

// NewServer creates a new registar server.
// It configures the server to use the provided registar backend.
func NewServer(registar Registar) *Server {
	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		H3:        &http3.Server{},
		registar:  registar,
		ctx:       ctx,
		ctxCancel: cancel,
	}
}

func (s *Server) Host(w http.ResponseWriter, r *http.Request, key auth.Key) {
	s.register(w, r, key, func(conn *quic.Conn, addrs AddrPair) error {
		peer := newPeer(addrs, conn)
		return s.registar.Host(r.Context(), key, peer)
	})
}

func (s *Server) Join(w http.ResponseWriter, r *http.Request, key auth.Key) {
	s.register(w, r, key, func(conn *quic.Conn, addrs AddrPair) error {
		peer := newPeer(addrs, conn)
		return s.registar.Join(r.Context(), key, peer)
	})
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
	})
}

type callback = func(conn *quic.Conn, addrs AddrPair) error

func (s *Server) register(w http.ResponseWriter, r *http.Request, key auth.Key, cb callback) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	caCert, cert, err := s.registar.NewSessionCert(key, (*x509.CertificateRequest)(req.CSR))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := RegisterResponse{
		CACert: (*Certificate)(caCert),
		Cert:   (*Certificate)(cert),
	}

	conn, ok := r.Context().Value(quicConnContextKey).(*quic.Conn)
	if !ok {
		http.Error(w, "no quic connection in context", http.StatusInternalServerError)
		return
	}

	addrs := AddrPair{
		Public:  conn.RemoteAddr().(*net.UDPAddr).AddrPort(),
		Private: req.PrivateAddr,
	}

	if err := cb(conn, addrs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (s *Server) Serve(conn net.PacketConn) error {
	var quicConf *quic.Config
	if s.H3.QUICConfig != nil {
		quicConf = s.H3.QUICConfig.Clone()
	} else {
		quicConf = &quic.Config{}
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
				if errors.Is(err, context.Canceled) {
					return nil
				}
				if isRemoteConnClosed(err) {
					return nil
				}
				if isLocalPeerClosed(err) {
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
			str, err := conn.AcceptUniStream(ctx) // control streams
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				if isRemoteConnClosed(err) {
					return nil
				}
				if isLocalPeerClosed(err) {
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
	s.registar.Close()

	s.ctxCancel()

	err := s.H3.Close()
	s.connsCount.Wait()
	return err
}

// NewRouter creates a new HTTP handler for the registar server.
func NewRouter(srv *Server, secret []byte) *http.ServeMux {
	hostHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _ := auth.KeyFromContext(r.Context())

		srv.Host(w, r, key)
	})
	joinHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _ := auth.KeyFromContext(r.Context())

		srv.Join(w, r, key)
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

	return mux
}

func isRemoteConnClosed(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) &&
		appErr.ErrorCode == 0 &&
		appErr.Remote
}

func isLocalPeerClosed(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) &&
		appErr.ErrorCode == 0 &&
		!appErr.Remote
}

type peer struct {
	addrs      AddrPair
	controller *control.Controller
	conn       *quic.Conn
}

var _ Peer = (*peer)(nil)

func newPeer(addrs AddrPair, conn *quic.Conn) peer {
	return peer{
		addrs:      addrs,
		controller: control.NewController(conn),
		conn:       conn,
	}
}

func (p peer) Addrs() AddrPair {
	return p.addrs
}

func (p peer) ConnectTo(ctx context.Context, public, private netip.AddrPort) error {
	return p.controller.ConnectTo(ctx, public, private)
}

func (p peer) Context() context.Context {
	return p.conn.Context()
}

func (p peer) Close(err error) {
	p.conn.CloseWithError(errCodePeerClosed, err.Error())
}
