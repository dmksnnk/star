package relay_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"github.com/dmksnnk/star/internal/relay"
	"github.com/quic-go/quic-go"
	"go.uber.org/goleak"
	"golang.org/x/sync/errgroup"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestUDPRelay(t *testing.T) {
	relayConn := integrationtest.NewLocalUDPConn(t)
	clientAConn := integrationtest.NewLocalUDPConn(t)
	clientBConn := integrationtest.NewLocalUDPConn(t)

	relayAddr := relayConn.LocalAddr().(*net.UDPAddr).AddrPort()

	t.Run("forwards packets", func(t *testing.T) {
		r := runRelay(t, relayConn, 100*time.Millisecond)

		r.AddRoute(
			clientAConn.LocalAddr().(*net.UDPAddr).AddrPort(),
			clientBConn.LocalAddr().(*net.UDPAddr).AddrPort(),
		)

		message := []byte("hello, B!")
		if _, err := clientAConn.WriteToUDPAddrPort(message, relayAddr); err != nil {
			t.Errorf("write to relay: %v", err)
		}

		buf := make([]byte, 1500)
		clientBConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		n, from, err := clientBConn.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatalf("read from relay: %v", err)
		}

		if from != relayAddr {
			t.Errorf("expected packet from relay %s, got %s", relayAddr, from)
		}

		received := buf[:n]
		if string(received) != string(message) {
			t.Errorf("expected message %q, got %q", message, received)
		}

		message = []byte("hello, A!")
		if _, err := clientBConn.WriteToUDPAddrPort(message, relayAddr); err != nil {
			t.Errorf("write to relay: %v", err)
		}

		clientAConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, from, err = clientAConn.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatalf("read from relay: %s", err)
		}

		if from != relayAddr {
			t.Errorf("expected packet from relay %s, got %s", relayAddr, from)
		}

		received = buf[:n]
		if string(received) != string(message) {
			t.Errorf("expected message %q, got %q", message, received)
		}
	})

	t.Run("route eviction", func(t *testing.T) {
		r := runRelay(t, relayConn, 50*time.Millisecond)

		r.AddRoute(
			clientAConn.LocalAddr().(*net.UDPAddr).AddrPort(),
			clientBConn.LocalAddr().(*net.UDPAddr).AddrPort(),
		)

		time.Sleep(100 * time.Millisecond)

		message := []byte("hello, relay!")
		if _, err := clientAConn.WriteToUDPAddrPort(message, relayAddr); err != nil {
			t.Errorf("write to relay: %v", err)
		}

		buf := make([]byte, 1500)
		clientBConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		_, _, err := clientBConn.ReadFromUDPAddrPort(buf)
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("expected read timeout, got: %s", err)
		}
	})
}

func TestUDPRelay_RelayQUICTraffic(t *testing.T) {
	clientAConn := integrationtest.NewLocalUDPConn(t)
	clientBConn := integrationtest.NewLocalUDPConn(t)

	relay, relayAddr := integrationtest.ServeRelay(t)
	relay.AddRoute(
		clientAConn.LocalAddr().(*net.UDPAddr).AddrPort(),
		clientBConn.LocalAddr().(*net.UDPAddr).AddrPort(),
	)

	serverTLSConf, clientTLSConf := generateTLSConfig(t)
	var eg errgroup.Group
	eg.Go(func() error {
		conn, err := quic.Dial(context.TODO(), clientAConn, net.UDPAddrFromAddrPort(relayAddr), clientTLSConf, &quic.Config{})
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}

		stream, err := conn.OpenStreamSync(context.TODO())
		if err != nil {
			return fmt.Errorf("open stream: %w", err)
		}
		_, err = stream.Write([]byte("hello from A"))
		if err != nil {
			return fmt.Errorf("write stream: %w", err)
		}

		return nil
	})

	eg.Go(func() error {
		listener, err := quic.Listen(clientBConn, serverTLSConf, &quic.Config{})
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}

		defer listener.Close()

		conn, err := listener.Accept(context.TODO())
		if err != nil {
			return fmt.Errorf("accept stream: %w", err)
		}

		stream, err := conn.AcceptStream(context.TODO())
		if err != nil {
			return fmt.Errorf("accept stream: %w", err)
		}

		buf := make([]byte, 1024)
		n, err := stream.Read(buf)
		if err != nil {
			return fmt.Errorf("read stream: %w", err)
		}
		received := string(buf[:n])
		if received != "hello from A" {
			return fmt.Errorf("unexpected message: %s", received)
		}
		return nil
	})

	if err := eg.Wait(); err != nil {
		t.Fatalf("quic relay: %s", err)
	}
}

func TestUDPRelayAllocations(t *testing.T) {
	clientAConn := integrationtest.NewLocalUDPConn(t)
	clientBConn := integrationtest.NewLocalUDPConn(t)

	relay, relayAddr := integrationtest.ServeRelay(t)
	relay.AddRoute(
		clientAConn.LocalAddr().(*net.UDPAddr).AddrPort(),
		clientBConn.LocalAddr().(*net.UDPAddr).AddrPort(),
	)

	buf := make([]byte, 1500)
	message := []byte("hello, allocs!")

	allocs := testing.AllocsPerRun(100, func() {
		if _, err := clientAConn.WriteToUDPAddrPort(message, relayAddr); err != nil {
			t.Fatalf("write to relay: %s", err)
		}

		clientBConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _, err := clientBConn.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatalf("read from relay: %s", err)
		}
		if n == 0 {
			t.Fatalf("read 0 bytes")
		}
	})

	if allocs > 0 {
		t.Errorf("expected at 0 allocations per run, got %f", allocs)
	}
}

func runRelay(t *testing.T, relayConn *net.UDPConn, ttl time.Duration) *relay.UDPRelay {
	r := relay.NewUDPRelay(relay.WithRouteTTL(ttl))

	var eg errgroup.Group
	eg.Go(func() error {
		return r.Serve(relayConn)
	})

	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close relay: %v", err)
		}

		if err := eg.Wait(); err != nil {
			t.Errorf("serve relay: %s", err)
		}
	})

	return r
}

func generateTLSConfig(t *testing.T) (serverTLSConf *tls.Config, clientTLSConf *tls.Config) {
	t.Helper()
	// Create a new private key
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key: %s", err)
	}

	// Create a self-signed certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Test Co"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Create the self-signed certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %s", err)
	}

	// Encode the private key and certificate to PEM format
	privBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	certBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	// Create the TLS certificate
	tlsCert, err := tls.X509KeyPair(certBytes, privBytes)
	if err != nil {
		t.Fatalf("create TLS cert: %s", err)
	}

	// Server config
	serverTLSConf = &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"star-quic-relay-test"},
	}

	// Client config
	clientTLSConf = &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"star-quic-relay-test"},
	}

	return serverTLSConf, clientTLSConf
}
