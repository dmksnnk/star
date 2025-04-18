package cert

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"math/big"
	"net"
	"time"
)

// NewCert generates a new certificate for the given IP address signed by the given CA.
func NewCert(ca *x509.Certificate, caPrivateKey crypto.PrivateKey, req *x509.CertificateRequest) ([]byte, error) {
	if err := req.CheckSignature(); err != nil {
		return nil, fmt.Errorf("check signature: %w", err)
	}

	serialNumber, err := newSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(3 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// Both client and server
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,

		SignatureAlgorithm: req.SignatureAlgorithm,
		Subject:            req.Subject,
		// SANs
		DNSNames:        req.DNSNames,
		EmailAddresses:  req.EmailAddresses,
		IPAddresses:     req.IPAddresses,
		URIs:            req.URIs,
		ExtraExtensions: req.ExtraExtensions,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, ca, req.PublicKey, caPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	return certBytes, nil
}

// NewCert generates a new certificate for the given IP address signed by the given CA.
func NewIPCert(ca *x509.Certificate, caPrivateKey crypto.PrivateKey, ip net.IP, pubKey crypto.PublicKey) ([]byte, error) {
	serialNumber, err := newSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(3 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// Both client and server
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,

		// SANs
		IPAddresses: []net.IP{ip},
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, ca, pubKey, caPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	return certBytes, nil
}

func newSerialNumber() (*big.Int, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialNumberLimit)
}
