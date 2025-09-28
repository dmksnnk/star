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
	"net/netip"
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

func RegisterClient(ctx context.Context, tlsConf *tls.Config, base *url.URL, secret []byte, key auth.Key) (*RegisteredClient, error) {
	tlsConfig := http3.ConfigureTLSConfig(tlsConf)
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
	conn, err := quic.DialAddr(ctx, addr, tlsConfig, quicConfig)
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

	localAddr := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	req := RegisterRequest{
		PrivateAddr: localAddr,
		CSR:         (*CSR)(csr),
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		return nil, fmt.Errorf("encode body: %w", err)
	}

	u := base.ResolveReference(&url.URL{Path: "/register"}).String()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &buf)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	token := auth.NewToken(key, secret)
	httpReq.Header.Set(headerToken, token.String())

	httpResp, err := clientConn.RoundTrip(httpReq)
	if err != nil {
		return nil, fmt.Errorf("round trip: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, httpplatform.NewBadStatusCodeError(httpResp.StatusCode, httpResp.Body)
	}

	var resp RegisterResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &RegisteredClient{
		base:   base,
		token:  token,
		conn:   clientConn,
		CACert: (*x509.Certificate)(resp.CACert),
		Cert:   (*x509.Certificate)(resp.Cert),
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

	CACert *x509.Certificate
	Cert   *x509.Certificate
}

func (rc *RegisteredClient) Host(ctx context.Context) (*http3.RequestStream, error) {
	reqStream, err := rc.sendRequest(ctx, "/host")
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

func (rc *RegisteredClient) Join(ctx context.Context) (*http3.RequestStream, error) {
	reqStream, err := rc.sendRequest(ctx, "/join")
	if err != nil {
		return nil, fmt.Errorf("send join request: %w", err)
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

func (rc *RegisteredClient) LocalAddr() netip.AddrPort {
	return rc.conn.Conn().LocalAddr().(*net.UDPAddr).AddrPort()
}

func (rc *RegisteredClient) sendRequest(ctx context.Context, path string) (*http3.RequestStream, error) {
	reqStream, err := rc.conn.OpenRequestStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open request stream: %w", err)
	}

	u := rc.resolvePath(path)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set(headerToken, rc.token.String())

	if err := reqStream.SendRequestHeader(httpReq); err != nil {
		return nil, fmt.Errorf("send request header: %w", err)
	}

	return reqStream, nil
}

func (rc *RegisteredClient) resolvePath(path string) string {
	return rc.base.ResolveReference(&url.URL{Path: path}).String()
}
