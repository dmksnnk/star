package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/netip"
)

// Config defines the configuration for the peer test program.
type Config struct {
	Name               string
	ListenAddress      netip.AddrPort
	PeerPublicAddress  netip.AddrPort
	PeerPrivateAddress netip.AddrPort
	Cert               Cert
	Mode               string
}

type Cert struct {
	CACert     []byte
	Cert       []byte
	PrivateKey []byte
}

// TLSConfig creates TLS config from raw bytes.
func (c Cert) TLSConfig() (*tls.Config, error) {
	caCert, err := x509.ParseCertificate(c.CACert)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	privKey, err := x509.ParsePKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parser private key: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{c.Cert},
				PrivateKey:  privKey,
			},
		},
		RootCAs: pool,
	}, nil
}
