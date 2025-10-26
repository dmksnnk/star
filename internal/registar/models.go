package registar

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/netip"
)

type CertRequest struct {
	CSR *CSR `json:"csr"`
}

type CertResponse struct {
	CACert *Certificate `json:"ca_cert"`
	Cert   *Certificate `json:"cert"`
}

type CSR x509.CertificateRequest

func (c *CSR) MarshalText() ([]byte, error) {
	var buf bytes.Buffer
	err := pem.Encode(&buf, &pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: c.Raw,
	})
	if err != nil {
		return nil, fmt.Errorf("encode certificate: %w", err)
	}

	return buf.Bytes(), nil
}

func (c *CSR) UnmarshalText(data []byte) error {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return fmt.Errorf("invalid certificate request")
	}

	certReq, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse certificate request: %w", err)
	}

	*c = CSR(*certReq)

	return nil
}

type Certificate x509.Certificate

func (c *Certificate) MarshalText() ([]byte, error) {
	var buf bytes.Buffer
	err := pem.Encode(&buf, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: c.Raw,
	})
	if err != nil {
		return nil, fmt.Errorf("encode certificate: %w", err)
	}

	return buf.Bytes(), nil
}

func (c *Certificate) UnmarshalText(data []byte) error {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("invalid certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}

	*c = Certificate(*cert)

	return nil
}

type RegisterRequest struct {
	PrivateAddr netip.AddrPort
	PublicAddr  netip.AddrPort
}

func (r *RegisterRequest) UnmarshalHTTP(req *http.Request) error {
	var err error

	r.PrivateAddr, err = netip.ParseAddrPort(req.URL.Query().Get("private_addr"))
	if err != nil {
		return fmt.Errorf("parse private addr: %w", err)
	}

	r.PublicAddr, err = netip.ParseAddrPort(req.URL.Query().Get("public_addr"))
	if err != nil {
		return fmt.Errorf("parse public addr: %w", err)
	}

	return nil
}

func (r *RegisterRequest) MarshalHTTP(req *http.Request) error {
	q := req.URL.Query()
	q.Set("private_addr", r.PrivateAddr.String())
	q.Set("public_addr", r.PublicAddr.String())
	req.URL.RawQuery = q.Encode()

	return nil
}
