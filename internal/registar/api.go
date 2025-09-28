package registar

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	pathValuePeerID = "peerID"
	headerToken     = "Token"
)

// API is a HTTP API for registar service.
type API struct {
	registeredAddrs *KVStore[quic.ConnectionTracingID, Waiter[AddrPair]]
	registeredWg    sync.WaitGroup

	registar    Registar
	caAuthority *Authority
}

type Registar interface {
	Host(ctx context.Context, key auth.Key, streamer http3.HTTPStreamer, addrs AddrPair) error
	Join(ctx context.Context, key auth.Key, streamer http3.HTTPStreamer, addrs AddrPair) error
}

// New creates a new API on top of the given service.
func NewAPI(r Registar, caAuthority *Authority) *API {
	return &API{
		registeredAddrs: NewKVStore[quic.ConnectionTracingID, Waiter[AddrPair]](),

		registar:    r,
		caAuthority: caAuthority,
	}
}

// TODO: create client, write tests

func (a *API) Register(w http.ResponseWriter, r *http.Request) {
	if !a.validateHTTP3Settings(w) {
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	addrs := AddrPair{
		Public:  remoteAddr(w, r),
		Private: req.PrivateAddr,
	}

	key, _ := auth.KeyFromContext(r.Context())

	// TODO: move to Registar, remove session CA on session end
	caCert, cert, err := a.caAuthority.NewSessionCert(key, addrs, (*x509.CertificateRequest)(req.CSR))
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

	// TODO: move to Registar, to make API stateless
	conn := w.(http3.Hijacker).Connection()
	connTracingID := conn.Context().Value(quic.ConnectionTracingKey).(quic.ConnectionTracingID)
	fmt.Println("api: waiting for", connTracingID, "to host/join")

	addrsWaiter := newWaiter(addrs)
	a.registeredAddrs.Set(connTracingID, addrsWaiter)

	a.registeredWg.Add(1)
	go func() {
		defer a.registeredWg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		// either done or timeout, delete from map in both cases
		_ = addrsWaiter.Wait(ctx)
		fmt.Printf("api: done waiting for %d to host/join\n", connTracingID)

		a.registeredAddrs.Delete(connTracingID)
	}()
}

func (a *API) Host(w http.ResponseWriter, r *http.Request) {
	conn := w.(http3.Hijacker).Connection()
	connTracingID := conn.Context().Value(quic.ConnectionTracingKey).(quic.ConnectionTracingID)

	addrsWaiter, ok := a.registeredAddrs.Get(connTracingID)
	if !ok {
		http.Error(w, "no waiting host", http.StatusBadRequest)
		return
	}

	key, _ := auth.KeyFromContext(r.Context())

	streamer := w.(http3.HTTPStreamer)
	if err := a.registar.Host(r.Context(), key, streamer, addrsWaiter.V); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	addrsWaiter.Done() // done only after successful registration
}

func (a *API) Join(w http.ResponseWriter, r *http.Request) {
	conn := w.(http3.Hijacker).Connection()
	connTracingID := conn.Context().Value(quic.ConnectionTracingKey).(quic.ConnectionTracingID)

	addrsWaiter, ok := a.registeredAddrs.Get(connTracingID)
	if !ok {
		http.Error(w, "no waiting peer", http.StatusBadRequest)
		return
	}

	key, _ := auth.KeyFromContext(r.Context())

	streamer := w.(http3.HTTPStreamer)
	if err := a.registar.Join(r.Context(), key, streamer, addrsWaiter.V); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	addrsWaiter.Done() // done only after successful joining, so client can retry on error
}

func (a *API) Close() error {
	a.registeredWg.Wait()
	return nil
}

func remoteAddr(w http.ResponseWriter, r *http.Request) netip.AddrPort {
	// FIXME: can public add be from proxy?
	conn := w.(http3.Hijacker).Connection()

	return conn.RemoteAddr().(*net.UDPAddr).AddrPort()
}

// // Forward forwards a peer's conn to the game host conn.
// func (a API) Forward(w http.ResponseWriter, r *http.Request) {
// 	if !a.validateHTTP3Settings(w) {
// 		return
// 	}

// 	key, _ := auth.KeyFromContext(r.Context())
// 	peerID := r.PathValue(pathValuePeerID)
// 	if peerID == "" {
// 		http.Error(w, "missing peer id", http.StatusBadRequest)
// 		return
// 	}

// 	streamer := w.(http3.HTTPStreamer)
// 	if err := a.service.Forward(r.Context(), key, peerID, streamer); err != nil {
// 		if errors.Is(err, registar.ErrPeerNotFound) {
// 			http.Error(w, err.Error(), http.StatusNotFound)
// 			return
// 		}

// 		http.Error(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// }

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
		"POST /register",
		httpplatform.Wrap(
			http.HandlerFunc(api.Register),
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

	// mux.Handle(
	// 	"CONNECT /games/{token}/forward/{peerID}",
	// 	httpplatform.Wrap(
	// 		http.HandlerFunc(api.Forward),
	// 		httpplatform.Authenticate(secret, httpplatform.TokenFromPathValue("token")),
	// 	),
	// )

	return mux
}

type Waiter[T any] struct {
	V T
	c chan struct{}
}

func newWaiter[T any](v T) Waiter[T] {
	return Waiter[T]{
		V: v,
		c: make(chan struct{}),
	}
}

func (w Waiter[T]) Wait(ctx context.Context) bool {
	select {
	case <-w.c:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w Waiter[T]) Done() {
	close(w.c)
}

type KVStore[K comparable, V any] struct {
	mu   sync.RWMutex
	data map[K]V
}

func NewKVStore[K comparable, V any]() *KVStore[K, V] {
	return &KVStore[K, V]{
		data: make(map[K]V),
	}
}

func (s *KVStore[K, V]) Get(key K) (V, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	v, ok := s.data[key]
	return v, ok
}

func (s *KVStore[K, V]) Set(key K, v V) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[key] = v
}

func (s *KVStore[K, V]) Delete(key K) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data, key)
}
