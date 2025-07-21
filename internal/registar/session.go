package registar

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"time"

	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

const defaultConnectTimeout = time.Minute

type Session struct {
	host Host
}

func NewSession(host Host) *Session {
	return &Session{
		host: host,
	}
}

// Join returns true if p2p connection is established, false it via relay.
func (s *Session) Join(
	ctx context.Context,
	peerID string,
	publicAddress netip.AddrPort,
	privateAddress netip.AddrPort,
	controlStream *http3.Stream,
) (bool, error) {
	peerCtrl := control.NewController(controlStream)

	if err := s.initP2P(ctx, publicAddress, privateAddress, peerCtrl); err != nil {
		return false, s.initRelayConnection(ctx, peerID, peerCtrl)
	}

	return true, nil
}

func (s *Session) initP2P(
	ctx context.Context,
	publicAddress netip.AddrPort,
	privateAddress netip.AddrPort,
	peerCtrl *control.Controller,
) error {
	ctx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return s.host.control.ConnectTo(ctx, publicAddress, privateAddress)
	})
	eg.Go(func() error {
		return peerCtrl.ConnectTo(ctx, s.host.public, s.host.private)
	})

	return eg.Wait()
}

func (s *Session) initRelayConnection(ctx context.Context, peerID string, peerCtrl *control.Controller) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultConnectTimeout)
	defer cancel()

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return s.host.control.RequestRelayPeer(ctx, peerID)
	})
	eg.Go(func() error {
		return peerCtrl.JoinViaRelay(ctx)
	})

	return eg.Wait()
}

type Host struct {
	private, public netip.AddrPort
	control         *control.Controller
}

func NewHost(private, public netip.AddrPort, controlSteam *http3.Stream) Host {
	return Host{
		private: private,
		public:  public,
		control: control.NewController(controlSteam),
	}
}

type SessionCA struct {
	cert       *x509.Certificate
	privateKey crypto.PrivateKey
}

func NewSessionCA(cert *x509.Certificate, privateKey crypto.PrivateKey) *SessionCA {
	return &SessionCA{
		cert:       cert,
		privateKey: privateKey,
	}
}

func (s *SessionCA) NewCert(peerID string, public, private netip.AddrPort, csr *x509.CertificateRequest) ([]byte, error) {
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

		SignatureAlgorithm: csr.SignatureAlgorithm,
		Subject: pkix.Name{
			CommonName: peerID,
		},
		// SANs
		IPAddresses: []net.IP{net.ParseIP(public.Addr().String()), net.ParseIP(private.Addr().String())},
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
