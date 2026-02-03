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
	"path"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

type ClientConfig struct {
	QUICConfig *quic.Config
	// TLSConfig for calling registar API.
	TLSConfig *tls.Config
}

func (cc ClientConfig) Join(
	ctx context.Context,
	quicTransport *quic.Transport,
	base *url.URL,
	token auth.Token,
) (*quic.Conn, *tls.Config, error) {
	return cc.register(ctx, quicTransport, base, token, "/join")
}

func (cc ClientConfig) Host(
	ctx context.Context,
	quicTransport *quic.Transport,
	base *url.URL,
	token auth.Token,
) (*quic.Conn, *tls.Config, error) {
	return cc.register(ctx, quicTransport, base, token, "/host")
}

func (cc ClientConfig) register(
	ctx context.Context,
	quicTransport *quic.Transport,
	base *url.URL,
	token auth.Token,
	path string,
) (*quic.Conn, *tls.Config, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate private key: %w", err)
	}

	csr, err := newCSR(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create CSR: %w", err)
	}

	addr := base.Host
	updAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve UDP address: %w", err)
	}

	tlsConf := cc.TLSConfig
	if tlsConf == nil {
		tlsConf = &tls.Config{
			NextProtos: []string{http3.NextProtoH3}, // set the ALPN for HTTP/3
		}
	}

	conn, err := quicTransport.Dial(ctx, updAddr, tlsConf, cc.QUICConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("dial QUIC: %w", err)
	}
	// no set up of HTTP3 transport here, no need for HTTP datagrams (only quic datagrams)
	tr := &http3.Transport{}
	clientConn := tr.NewClientConn(conn)

	req := RegisterRequest{
		PrivateAddr: conn.LocalAddr().(*net.UDPAddr).AddrPort(),
		CSR:         (*CSR)(csr),
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		return nil, nil, fmt.Errorf("encode body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolvePath(base, path), &buf)
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set(headerToken, token.String())

	httpResp, err := clientConn.RoundTrip(httpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("round trip: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, nil, httpplatform.NewBadStatusCodeError(httpResp.StatusCode, httpResp.Body)
	}

	var resp RegisterResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, nil, fmt.Errorf("decode response: %w", err)
	}

	return conn, tlsConfig((*x509.Certificate)(resp.CACert), (*x509.Certificate)(resp.Cert), privateKey), nil
}

func resolvePath(base *url.URL, sub string) string {
	ref := &url.URL{
		Path: path.Join(base.Path, sub),
	}

	return base.ResolveReference(ref).String()
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
