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
	"net"
	"net/http"
	"net/url"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// ErrDatagramsNotEnabled is returned when client expects datagrams to be enabled on the server,
// but they are not.
var ErrDatagramsNotEnabled = errors.New("datagrams are not enabled")

// RegisterClient registers a new client with the registar server and obtains a session certificate.
// Client is only valid of a session.
func RegisterClient(ctx context.Context, quicTransport *quic.Transport, tlsConf *tls.Config, base *url.URL, token auth.Token) (*RegisteredClient, error) {
	quicConfig := &quic.Config{
		EnableDatagrams: true,
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}

	csr, err := newCSR(privateKey)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}

	addr := base.Host
	updAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP address: %w", err)
	}

	conn, err := quicTransport.Dial(ctx, updAddr, tlsConf, quicConfig)
	if err != nil {
		return nil, fmt.Errorf("dial QUIC: %w", err)
	}

	tr := &http3.Transport{
		EnableDatagrams: quicConfig.EnableDatagrams,
	}
	clientConn := tr.NewClientConn(conn)
	select {
	case <-clientConn.ReceivedSettings():
	case <-clientConn.Context().Done():
		return nil, clientConn.Context().Err()
	}

	settings := clientConn.Settings()
	if !settings.EnableDatagrams {
		if err := clientConn.CloseWithError(errcode.Exit, ErrDatagramsNotEnabled.Error()); err != nil {
			return nil, fmt.Errorf("close connection: %w", err)
		}
		return nil, ErrDatagramsNotEnabled
	}

	req := CertRequest{
		CSR: (*CSR)(csr),
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		return nil, fmt.Errorf("encode body: %w", err)
	}

	u := base.ResolveReference(&url.URL{Path: "/session-cert"}).String()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &buf)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set(headerToken, token.String())

	httpResp, err := clientConn.RoundTrip(httpReq)
	if err != nil {
		return nil, fmt.Errorf("round trip: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, httpplatform.NewBadStatusCodeError(httpResp.StatusCode, httpResp.Body)
	}

	var resp CertResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &RegisteredClient{
		base:       base,
		token:      token,
		conn:       clientConn,
		CACert:     (*x509.Certificate)(resp.CACert),
		Cert:       (*x509.Certificate)(resp.Cert),
		privateKey: privateKey,
	}, nil
}

func newCSR(privateKey *ecdsa.PrivateKey) (*x509.CertificateRequest, error) {
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: "example.com",
		},
		DNSNames: []string{"example.com", "www.example.com"},
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

type RegisteredClient struct {
	base  *url.URL
	token auth.Token
	conn  *http3.ClientConn

	CACert     *x509.Certificate
	Cert       *x509.Certificate
	privateKey *ecdsa.PrivateKey
}

func (rc *RegisteredClient) Host(ctx context.Context, addrs AddrPair) (*http3.RequestStream, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, rc.resolvePath("/host"), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req := RegisterRequest{
		PrivateAddr: addrs.Private,
		PublicAddr:  addrs.Public,
	}
	if err := req.MarshalHTTP(httpReq); err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqStream, err := rc.sendRequest(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("send host request: %w", err)
	}

	httpResp, err := reqStream.ReadResponse()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, httpplatform.NewBadStatusCodeError(httpResp.StatusCode, httpResp.Body)
	}

	return reqStream, nil
}

func (rc *RegisteredClient) Join(ctx context.Context, addrs AddrPair) (*http3.RequestStream, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, rc.resolvePath("/join"), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req := RegisterRequest{
		PrivateAddr: addrs.Private,
		PublicAddr:  addrs.Public,
	}
	if err := req.MarshalHTTP(httpReq); err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	reqStream, err := rc.sendRequest(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	httpResp, err := reqStream.ReadResponse()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, httpplatform.NewBadStatusCodeError(httpResp.StatusCode, httpResp.Body)
	}

	return reqStream, nil
}

// TLSConfig returns a tls.Config that can be used to connect between peers.
func (rc *RegisteredClient) TLSConfig() *tls.Config {
	rootPool := x509.NewCertPool()
	rootPool.AddCert(rc.CACert)

	return &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{rc.Cert.Raw},
				PrivateKey:  rc.privateKey,
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

func (rc *RegisteredClient) Close() error {
	return rc.conn.CloseWithError(errcode.Exit, "client closed")
}

func (rc *RegisteredClient) sendRequest(ctx context.Context, req *http.Request) (*http3.RequestStream, error) {
	reqStream, err := rc.conn.OpenRequestStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open request stream: %w", err)
	}

	req.Header.Set(headerToken, rc.token.String())

	if err := reqStream.SendRequestHeader(req); err != nil {
		return nil, fmt.Errorf("send request header: %w", err)
	}

	return reqStream, nil
}

func (rc *RegisteredClient) resolvePath(path string) string {
	return rc.base.ResolveReference(&url.URL{Path: path}).String()
}
