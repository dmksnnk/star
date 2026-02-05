package registar

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/dmksnnk/star/internal/registar/auth"
)

type RootCA struct {
	cert    *x509.Certificate
	privKey crypto.PrivateKey
}

func NewRootCA() (RootCA, error) {
	serialNumber, err := newSerialNumber()
	if err != nil {
		return RootCA{}, fmt.Errorf("generate serial number: %w", err)
	}

	rootPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return RootCA{}, fmt.Errorf("generate private key: %w", err)
	}

	rootTemplate := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageCertSign,
		Subject: pkix.Name{
			Organization: []string{"Registar"},
		},
		// constraints
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &rootTemplate, &rootTemplate, &rootPrivateKey.PublicKey, rootPrivateKey)
	if err != nil {
		return RootCA{}, fmt.Errorf("create certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return RootCA{}, fmt.Errorf("parse certificate: %w", err)
	}

	return RootCA{
		cert:    cert,
		privKey: rootPrivateKey,
	}, nil
}

func (r *RootCA) NewSessionCA() (*SessionCA, error) {
	serialNumber, err := newSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageCertSign,

		Subject: pkix.Name{
			Organization: []string{"Registar Intermediate CA"},
		},

		// constraints
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, r.cert, &privKey.PublicKey, r.privKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}

	return newSessionCA(cert, privKey), nil
}

type SessionCA struct {
	cert       *x509.Certificate
	privateKey crypto.PrivateKey
}

func newSessionCA(cert *x509.Certificate, privateKey crypto.PrivateKey) *SessionCA {
	return &SessionCA{
		cert:       cert,
		privateKey: privateKey,
	}
}

func (s *SessionCA) NewCert(csr *x509.CertificateRequest) ([]byte, error) {
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("check signature: %w", err)
	}

	serialNumber, err := newSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(0, 0, 7), // 7 days
		KeyUsage:     x509.KeyUsageDigitalSignature,
		// Both client and server
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,

		Subject: csr.Subject,
		// SANs
		DNSNames:       csr.DNSNames,
		IPAddresses:    csr.IPAddresses,
		EmailAddresses: csr.EmailAddresses,
		URIs:           csr.URIs,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, s.cert, csr.PublicKey, s.privateKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	return certBytes, nil
}

func newSerialNumber() (*big.Int, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialNumberLimit)
}

// Authority issues new session certificates.
type Authority struct {
	root       RootCA
	mux        sync.Mutex
	sessionsCA map[auth.Key]*SessionCA
}

func NewAuthority(root RootCA) *Authority {
	return &Authority{
		root:       root,
		sessionsCA: make(map[auth.Key]*SessionCA),
	}
}

// NewSessionCert returns a session's CA certificate and peer's certificate.
// Peer should trust the session's CA certificate.
func (a *Authority) NewSessionCert(key auth.Key, csr *x509.CertificateRequest) (caCert *x509.Certificate, peerCert *x509.Certificate, err error) {
	a.mux.Lock()
	defer a.mux.Unlock()

	ca, ok := a.sessionsCA[key]
	if !ok {
		sca, err := a.root.NewSessionCA()
		if err != nil {
			return nil, nil, fmt.Errorf("create session CA: %w", err)
		}
		a.sessionsCA[key] = sca
		ca = sca
	}

	certBytes, err := ca.NewCert(csr)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse certificate: %w", err)
	}

	return ca.cert, cert, nil
}

func (a *Authority) RemoveSessionCA(key auth.Key) {
	a.mux.Lock()
	defer a.mux.Unlock()

	delete(a.sessionsCA, key)
}
