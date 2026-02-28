package registar

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"time"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/p2p"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

var (
	ErrHosterClosed          = errors.New("hoster closed")
	ErrJoinerClosed          = errors.New("joiner closed")
	ErrHostNotFound          = errors.New("host not found")
	ErrHostAlreadyRegistered = errors.New("host already registered")
)

var errCodeClientClosed = quic.ApplicationErrorCode(0x1003)

// DatagramConn sends and receives datagrams.
type DatagramConn interface {
	ReceiveDatagram(context.Context) ([]byte, error)
	SendDatagram([]byte) error
	Close() error
}

type ClientConfig struct {
	// TLSConfig for calling registar API.
	TLSConfig *tls.Config
	// ListenAddr specifies the local UDP address to listen on for incoming QUIC connections.
	// If the IP field is nil or an unspecified IP address,
	// HostConfig listens on all available IP addresses of the local system
	// except multicast IP addresses.
	// If the Port field is 0, a port number is automatically chosen.
	ListenAddr *net.UDPAddr
	// KeepAlivePeriod sets the QUIC keep-alive period. Defaults to 10s.
	KeepAlivePeriod time.Duration
	// P2PDialTimeout sets the maximum idle timeout for P2P connection attempts.
	// A shorter value makes hole-punching fail faster when the remote is unreachable.
	// If zero, the quic-go default applies (~5s for the handshake phase).
	P2PDialTimeout time.Duration
	// DisableP2P disables P2P connection attempts, forcing relay fallback.
	// Intended for testing.
	DisableP2P bool
	Logger     *slog.Logger
}

// NewJoiner creates a Joiner that connects to the registar at base.
// It establishes the underlying QUIC+HTTP/3 connection but does not register yet;
// call Join to register and obtain a DatagramConn.
func (cc ClientConfig) NewJoiner(ctx context.Context, base *url.URL, token auth.Token) (*Joiner, error) {
	quicTransport, clientConn, privateAddr, err := cc.connect(ctx, base)
	if err != nil {
		return nil, err
	}

	logger := cc.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Joiner{
		base:           base,
		token:          token,
		p2pDialTimeout: cc.P2PDialTimeout,
		disableP2P:     cc.DisableP2P,
		quicTransport:  quicTransport,
		httpConn:       clientConn,
		privateAddr:    privateAddr,
		logger:         logger,
	}, nil
}

// NewHoster creates a Hoster that connects to the registar.
func (cc ClientConfig) NewHoster(ctx context.Context, base *url.URL, token auth.Token) (*Hoster, error) {
	quicTransport, clientConn, privateAddr, err := cc.connect(ctx, base)
	if err != nil {
		return nil, err
	}

	str, tlsConf, err := register(ctx, clientConn, base, token, "/host", privateAddr)
	if err != nil {
		quicTransport.Close()
		if isHostAlreadyRegisteredError(err) {
			return nil, ErrHostAlreadyRegistered
		}

		return nil, fmt.Errorf("register: %w", err)
	}

	logger := cc.Logger
	if logger == nil {
		logger = slog.Default()
	}

	connectorOpts := []p2p.Option{
		p2p.WithLogger(logger.With(slog.String("component", "p2p.Connector"))),
	}
	if cc.P2PDialTimeout > 0 {
		connectorOpts = append(connectorOpts, p2p.WithHandshakeIdleTimeout(cc.P2PDialTimeout))
	}
	connector := p2p.NewConnector(quicTransport, tlsConf, connectorOpts...)
	conns := make(chan DatagramConn, 1)
	agent := newControlAgent(connector, clientConn, base, token, conns, cc.DisableP2P, logger)

	var eg errgroup.Group
	eg.Go(func() error {
		if err := agent.Serve(str); err != nil {
			if isLocalHTTP3CloseError(err) {
				return nil
			}
			return fmt.Errorf("serve control agent: %w", err)
		}

		return nil
	})

	return &Hoster{
		eg:            &eg,
		quicTransport: quicTransport,
		httpConn:      clientConn,
		controlStream: str,
		conns:         conns,
		done:          make(chan struct{}),
	}, nil
}

// connect establishes the underlying QUIC+HTTP/3 connection to the registar at base.
// The caller is responsible for closing the returned transport on error.
func (cc ClientConfig) connect(ctx context.Context, base *url.URL) (*quic.Transport, *http3.ClientConn, netip.AddrPort, error) {
	addr := base.Host
	if base.Port() == "" {
		addr = net.JoinHostPort(base.Hostname(), "https")
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, nil, netip.AddrPort{}, fmt.Errorf("resolve UDP address: %w", err)
	}

	udpConn, err := net.ListenUDP("udp", cc.ListenAddr)
	if err != nil {
		return nil, nil, netip.AddrPort{}, fmt.Errorf("listen UDP: %w", err)
	}

	quicTransport := &quic.Transport{Conn: udpConn}

	keepAlive := cc.KeepAlivePeriod
	if keepAlive <= 0 {
		keepAlive = 10 * time.Second
	}

	quicConn, err := quicTransport.Dial(
		ctx,
		udpAddr,
		setupTLSConfig(cc.TLSConfig, base.Hostname()),
		&quic.Config{
			KeepAlivePeriod: keepAlive,
			EnableDatagrams: true,
		},
	)
	if err != nil {
		quicTransport.Close()
		return nil, nil, netip.AddrPort{}, fmt.Errorf("dial QUIC: %w", err)
	}

	tr := &http3.Transport{EnableDatagrams: true}
	clientConn := tr.NewClientConn(quicConn)

	// get local IP for registar connection: quic.Conn.LocalAddr() returns an unspecified
	// address because quic.Transport listens on all interfaces.
	localIP, err := getLocalAddr(udpAddr)
	if err != nil {
		quicTransport.Close()
		return nil, nil, netip.AddrPort{}, fmt.Errorf("get local address: %w", err)
	}
	localPort := quicConn.LocalAddr().(*net.UDPAddr).AddrPort().Port()

	return quicTransport, clientConn, netip.AddrPortFrom(localIP, localPort), nil
}

func register(ctx context.Context, conn *http3.ClientConn, base *url.URL, token auth.Token, path string, privateAddr netip.AddrPort) (*http3.RequestStream, *tls.Config, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate private key: %w", err)
	}

	csr, err := newCSR(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create CSR: %w", err)
	}

	req := RegisterRequest{
		PrivateAddr: privateAddr,
		CSR:         (*CSR)(csr),
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		return nil, nil, fmt.Errorf("encode body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolvePath(base, path), http.NoBody)
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set(headerToken, token.String())

	reqStream, err := conn.OpenRequestStream(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("open request stream: %w", err)
	}

	if err := reqStream.SendRequestHeader(httpReq); err != nil {
		return nil, nil, fmt.Errorf("send request header: %w", err)
	}

	stop := context.AfterFunc(ctx, func() {
		reqStream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		reqStream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	})
	defer stop()

	bodySentErr := make(chan error, 1)
	go func() {
		if _, err := io.Copy(reqStream, &buf); err != nil {
			bodySentErr <- fmt.Errorf("send request body: %w", err)
			return
		}

		bodySentErr <- nil
	}()

	httpResp, err := reqStream.ReadResponse()
	if err != nil {
		reqStream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		<-bodySentErr // wait for body to be sent or error
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		reqStream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		<-bodySentErr // wait for body to be sent or error
		return nil, nil, httpplatform.NewBadStatusCodeError(httpResp.StatusCode, httpResp.Body)
	}

	if err := <-bodySentErr; err != nil {
		return nil, nil, err
	}

	var resp RegisterResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, nil, fmt.Errorf("decode response: %w", err)
	}

	return reqStream, tlsConfig((*x509.Certificate)(resp.CACert), (*x509.Certificate)(resp.Cert), privateKey), nil
}

// newControlAgent creates an agent with P2P and relay handlers registered.
// It returns the agent and a channel that receives the established DatagramConn.
func newControlAgent(
	connector *p2p.Connector,
	httpConn *http3.ClientConn,
	base *url.URL,
	token auth.Token,
	conns chan<- DatagramConn,
	disableP2P bool,
	logger *slog.Logger,
) *control.Agent {
	agent := control.NewAgent()

	agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		if disableP2P {
			return false, nil
		}

		p2pConn, err := connector.Connect(ctx, cmd.PublicAddress, cmd.PrivateAddress)
		if err != nil {
			var idleErr *quic.IdleTimeoutError
			if errors.As(err, &idleErr) {
				logger.DebugContext(ctx, "p2p connection timed out", "public_addr", cmd.PublicAddress, "private_addr", cmd.PrivateAddress)
				return false, nil
			}

			logger.ErrorContext(ctx, "p2p connection failed", "error", err)
			return false, fmt.Errorf("connect to peer: %w", err)
		}

		select {
		case conns <- newQUICConnDatagramConn(p2pConn):
			logger.DebugContext(ctx, "established p2p connection", slog.String("remote_addr", p2pConn.RemoteAddr().String()))
			return true, nil
		case <-ctx.Done():
			p2pConn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeRequestCanceled), "rejected: context cancelled")
			return false, ctx.Err()
		}
	})

	agent.OnConnectViaRelay(func(ctx context.Context, sessionID string) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolvePath(base, "/relay/"+sessionID), http.NoBody)
		if err != nil {
			return fmt.Errorf("create relay request: %w", err)
		}
		req.Header.Set(headerToken, token.String())

		reqStream, err := httpConn.OpenRequestStream(ctx)
		if err != nil {
			return fmt.Errorf("open relay request stream: %w", err)
		}

		if err := reqStream.SendRequestHeader(req); err != nil {
			return fmt.Errorf("send relay request header: %w", err)
		}

		resp, err := reqStream.ReadResponse()
		if err != nil {
			return fmt.Errorf("read relay response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return httpplatform.NewBadStatusCodeError(resp.StatusCode, resp.Body)
		}

		select {
		case conns <- newStreamDatagramConn(reqStream):
			logger.DebugContext(ctx, "established relay connection")
			return nil
		case <-ctx.Done():
			reqStream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
			reqStream.Close()
			return ctx.Err()
		}
	})

	return agent
}

// Joiner registers with the registar as a peer and establishes a DatagramConn
// (P2P or relay). It owns the underlying QUIC transport and HTTP/3 connection,
// so Join can be called repeatedly for reconnections without re-dialing the registar.
type Joiner struct {
	base           *url.URL
	token          auth.Token
	p2pDialTimeout time.Duration
	disableP2P     bool
	quicTransport  *quic.Transport
	httpConn       *http3.ClientConn
	privateAddr    netip.AddrPort
	logger         *slog.Logger
}

// Join registers with the registar as a peer and waits for a DatagramConn
// to be established via P2P or relay. For reconnections, call Join again -
// it opens a new control stream on the existing QUIC connection.
func (j *Joiner) Join(ctx context.Context) (DatagramConn, error) {
	str, tlsConf, err := register(ctx, j.httpConn, j.base, j.token, "/join", j.privateAddr)
	if err != nil {
		if isHostNotFoundError(err) {
			return nil, ErrHostNotFound
		}

		return nil, fmt.Errorf("register: %w", err)
	}

	connectorOpts := []p2p.Option{
		p2p.WithLogger(j.logger.With(slog.String("component", "p2p.Connector"))),
	}
	if j.p2pDialTimeout > 0 {
		connectorOpts = append(connectorOpts, p2p.WithHandshakeIdleTimeout(j.p2pDialTimeout))
	}
	connector := p2p.NewConnector(j.quicTransport, tlsConf, connectorOpts...)
	conns := make(chan DatagramConn, 1)
	agent := newControlAgent(connector, j.httpConn, j.base, j.token, conns, j.disableP2P, j.logger)

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		if err := agent.Serve(str); err != nil {
			if isLocalHTTP3CloseError(err) {
				return nil
			}
			return fmt.Errorf("serve control agent: %w", err)
		}
		return nil
	})

	select {
	case conn := <-conns:
		str.CancelRead(quic.StreamErrorCode(http3.ErrCodeNoError)) // stop agent
		eg.Wait()                                                  // wait for agent to exit
		return conn, nil
	case <-ctx.Done():
		str.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		return nil, eg.Wait()
	}
}

// Close closes the joiner and all underlying resources.
func (j *Joiner) Close() error {
	j.httpConn.CloseWithError(quic.ApplicationErrorCode(errCodeClientClosed), "joiner closed")
	return j.quicTransport.Close()
}

type Hoster struct {
	eg            *errgroup.Group
	quicTransport *quic.Transport
	httpConn      *http3.ClientConn
	controlStream *http3.RequestStream
	conns         chan DatagramConn
	done          chan struct{}
}

// Accept waits for and returns the next incoming peer connection.
// Returns ErrHosterClosed when the hoster is closed.
func (h *Hoster) Accept(ctx context.Context) (DatagramConn, error) {
	select {
	case conn := <-h.conns:
		return conn, nil
	case <-h.done:
		return nil, ErrHosterClosed
	// case <-h.registarConn.Context().Done():
	// 	return nil, context.Cause(h.registarConn.Context())
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the control stream and waits for all underlying goroutines to exit.
// Accept will immediately unblock and return ErrHosterClosed.
func (h *Hoster) Close() error {
	h.controlStream.CancelRead(quic.StreamErrorCode(errCodeClientClosed))
	h.httpConn.CloseWithError(quic.ApplicationErrorCode(errCodeClientClosed), "hoster closed")
	h.quicTransport.Close()
	close(h.done)

	if err := h.eg.Wait(); err != nil {
		if isLocalHTTP3CloseError(err) {
			return nil
		}

		return fmt.Errorf("hoster error: %w", err)
	}

	return nil
}

func isLocalHTTP3CloseError(err error) bool {
	var quicErr *http3.Error
	return errors.As(err, &quicErr) &&
		quicErr.ErrorCode == http3.ErrCode(errCodeClientClosed) &&
		!quicErr.Remote
}

func isHostNotFoundError(err error) bool {
	return isStreamError(err, errCodeHostNotFound, true) ||
		isHTTP3Error(err, http3.ErrCode(errCodeHostNotFound), true)
}

func isHostAlreadyRegisteredError(err error) bool {
	return isStreamError(err, errCodeHostAlreadyRegistered, true) ||
		isHTTP3Error(err, http3.ErrCode(errCodeHostAlreadyRegistered), true)
}

func isStreamError(err error, code quic.StreamErrorCode, remote bool) bool {
	var quicErr *quic.StreamError
	return errors.As(err, &quicErr) &&
		quicErr.ErrorCode == code &&
		quicErr.Remote == remote
}

func isHTTP3Error(err error, code http3.ErrCode, remote bool) bool {
	var h3Err *http3.Error
	return errors.As(err, &h3Err) &&
		h3Err.ErrorCode == code &&
		h3Err.Remote == remote
}

func setupTLSConfig(conf *tls.Config, hostname string) *tls.Config {
	if conf == nil {
		return &tls.Config{
			ServerName: hostname,
			NextProtos: []string{http3.NextProtoH3}, // set the ALPN for HTTP/3
		}
	}

	conf = conf.Clone() // avoid mutating caller's config

	if conf.ServerName == "" { // if ServerName is not set, use the host part of the address we're connecting to.
		conf.ServerName = hostname
	}

	return conf
}

func resolvePath(base *url.URL, sub string) string {
	ref := &url.URL{
		Path: path.Join(base.Path, sub),
	}

	return base.ResolveReference(ref).String()
}

// getLocalAddr returns the local IP address that the OS would use to reach dest.
func getLocalAddr(dest *net.UDPAddr) (netip.Addr, error) {
	c, err := net.DialUDP("udp", nil, dest)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("dial UDP: %w", err)
	}
	defer c.Close()

	return c.LocalAddr().(*net.UDPAddr).AddrPort().Addr(), nil
}

func newCSR(privateKey *ecdsa.PrivateKey) (*x509.CertificateRequest, error) {
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: "star-p2p-peer",
		},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, privateKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate request: %w", err)
	}

	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, fmt.Errorf("parse certificate request: %w", err)
	}

	return csr, nil
}

// tlsConfig returns a [tls.Config] that can be used to connect between peers.
func tlsConfig(caCert, cert *x509.Certificate, privateKey *ecdsa.PrivateKey) *tls.Config {
	rootPool := x509.NewCertPool()
	rootPool.AddCert(caCert)

	return &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{cert.Raw},
				PrivateKey:  privateKey,
			},
		},
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  rootPool,
		RootCAs:    rootPool,
		NextProtos: []string{"star-p2p-1"},

		ServerName: "star",
		MinVersion: tls.VersionTLS13,

		// Disable hostname checking for P2P; we’ll verify the chain explicitly.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			// If we're the server, verifiedChains is already populated by Go's verifier.
			if len(verifiedChains) > 0 {
				return nil
			}

			// Client-side: verify the server's chain against our CA, without hostname.
			if len(rawCerts) == 0 {
				return errors.New("no server certificate")
			}

			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}

			inter := x509.NewCertPool()
			for _, b := range rawCerts[1:] {
				if c, err := x509.ParseCertificate(b); err == nil {
					inter.AddCert(c)
				}
			}
			_, err = leaf.Verify(x509.VerifyOptions{
				Roots:         rootPool,
				Intermediates: inter,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}
}

type quicConnDatagramConn struct {
	*quic.Conn
}

var _ DatagramConn = (*quicConnDatagramConn)(nil)

func newQUICConnDatagramConn(conn *quic.Conn) quicConnDatagramConn {
	return quicConnDatagramConn{Conn: conn}
}

func (q quicConnDatagramConn) Close() error {
	return q.CloseWithError(quic.ApplicationErrorCode(quic.NoError), "connection closed")
}

type streamDatagramConn struct {
	*http3.RequestStream
}

var _ DatagramConn = (*streamDatagramConn)(nil)

func newStreamDatagramConn(stream *http3.RequestStream) streamDatagramConn {
	return streamDatagramConn{RequestStream: stream}
}

func (s streamDatagramConn) Close() error {
	s.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	s.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	return nil
}
