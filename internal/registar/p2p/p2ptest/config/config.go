package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// Config defines the configuration for the peer test program.
type Config struct {
	ListenAddress      string
	PeerPublicAddress  string
	PeerPrivateAddress string
	Cert               Cert
	Mode               string
}

type Cert struct {
	CACert     []byte
	Cert       []byte
	PrivateKey []byte // TODO: encode private key with shared password using PKCS#12
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
