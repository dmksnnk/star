package forwarder_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/forwarder"
	"github.com/dmksnnk/star/internal/platform/quictest"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
)

func TestControlListener_P2PTimeout(t *testing.T) {
	// Control channel: simulates the registrar ↔ client connection.
	clientConn, serverConn := quictest.Pipe(t)

	// Separate P2P transport (in production this is the same UDP socket as the
	// control transport; a separate one is fine here since we only need ListenEarly
	// and Dial to work).
	p2pTR := integrationtest.NewLocalQUICTransport(t)

	// Bind a local UDP port and close it immediately to create a "dead" address:
	// quic-go does not receive ICMP port-unreachable via its standard ReadFrom,
	// so the dial will silently time out with IdleTimeoutError.
	deadUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	deadAddr := deadUDP.LocalAddr().(*net.UDPAddr).AddrPort()
	deadUDP.Close()

	clc := forwarder.ControlListenerConfig{
		Logger:         logger,
		P2PDialTimeout: 300 * time.Millisecond,
	}
	cl := clc.ListenControl(clientConn, p2pTR, newP2PTLSConf(t))
	t.Cleanup(func() {
		if err := cl.Close(); err != nil {
			t.Errorf("close control listener: %s", err)
		}
	})

	controller := control.NewController(serverConn)
	err = controller.ConnectTo(context.TODO(), deadAddr, deadAddr)
	if !errors.Is(err, control.ErrConnectFailed) {
		t.Fatalf("expected ErrConnectFailed, got: %s", err)
	}
}

// newP2PTLSConf returns a minimal mutual-TLS config suitable for the P2P
// connector in tests. The handshake never completes in this test (the dial
// times out), so we only need the config to be structurally valid.
func newP2PTLSConf(t *testing.T) *tls.Config {
	t.Helper()

	ca, caKey, err := cert.NewCA()
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	privKey, err := cert.NewPrivateKey()
	if err != nil {
		t.Fatalf("create private key: %v", err)
	}

	certDER, err := cert.NewIPCert(ca, caKey, privKey.Public(), net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca)

	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  privKey,
		}},
		RootCAs:            pool,
		ClientCAs:          pool,
		ClientAuth:         tls.RequireAndVerifyClientCert,
		InsecureSkipVerify: true, //nolint:gosec // P2P uses cert-chain verification, not hostname
		NextProtos:         []string{"star-p2p-1"},
		MinVersion:         tls.VersionTLS13,
	}
}
