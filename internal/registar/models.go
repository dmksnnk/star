package registar

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/netip"
)

type RegisterRequest struct {
	CSR         *CSR           `json:"csr"`
	PrivateAddr netip.AddrPort `json:"private_addr"`
}

type RegisterResponse struct {
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
