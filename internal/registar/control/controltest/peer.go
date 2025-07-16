package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
)

type config struct {
	ListenAddress      string   `env:"LISTEN_ADDRESS"`
	PeerPublicAddress  string   `env:"PEER_PUBLIC_ADDRESS"`
	PeerPrivateAddress string   `env:"PEER_PRIVATE_ADDRESS"`
	TLSIPs             []net.IP `env:"TLS_IPS"`
	Mode               string   `env:"MODE"`
}

func main() {
	var cfg config
	if err := env.Parse(&cfg); err != nil {
		slog.Error("parse config", "err", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	udpAddr, err := net.ResolveUDPAddr("udp", cfg.ListenAddress)
	if err != nil {
		slog.Error("resolve udp listen address", "err", err)
		os.Exit(1)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		slog.Error("listen UDP", "err", err)
		os.Exit(1)
	}

	transport := &quic.Transport{
		Conn: conn,
	}
	defer transport.Close()

	caTLSCert, err := caCert()
	if err != nil {
		slog.Error("create CA cert", "err", err)
		os.Exit(1)
	}

	tlsConfig, err := newTLSConfig(caTLSCert, cfg.TLSIPs)
	if err != nil {
		slog.Error("create TLS config", "err", err)
		os.Exit(1)
	}

	connector := control.NewConnector(transport, tlsConfig, control.WithLogger(logger))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := connector.Connect(ctx, cfg.PeerPrivateAddress, cfg.PeerPublicAddress)
	if err != nil {
		slog.Error("connector.Connect failed", "err", err)
		os.Exit(1)
	}
	slog.Info("connected")

	want := make([]byte, 1<<20)
	rand.Read(want)

	if cfg.Mode == "client" {
		runClient(stream)
	} else {
		runServer(stream)
	}
}

func runClient(stream http3platform.Stream) {
	want := make([]byte, 1<<20)
	rand.Read(want)

	_, err := io.CopyBuffer(stream, bytes.NewBuffer(want), make([]byte, 1024))
	if err != nil {
		slog.Error("write to stream", "err", err)
		os.Exit(1)
	}
	slog.Info("client: sent data")
	// close send direction
	if err := stream.Close(); err != nil {
		slog.Error("close stream", "err", err)
		os.Exit(1)
	}

	got := make([]byte, len(want))
	if _, err := io.ReadFull(stream, got); err != nil {
		slog.Error("client: read stream", "err", err)
		os.Exit(1)
	}
	slog.Info("client: received data")

	if !bytes.Equal(want, got) {
		slog.Error("data differs")
		os.Exit(1)
	}
}

func runServer(stream http3platform.Stream) {
	got, err := io.ReadAll(stream)
	if err != nil {
		slog.Error("read stream", "err", err)
		os.Exit(1)
	}
	slog.Info("server: received data")

	_, err = io.CopyBuffer(stream, bytes.NewBuffer(got), make([]byte, 1024))
	if err != nil {
		slog.Error("write to stream", "err", err)
		os.Exit(1)
	}
	if err := stream.Close(); err != nil {
		slog.Error("close stream", "err", err)
		os.Exit(1)
	}
	slog.Info("server: sent data")
}

func newTLSConfig(ca tls.Certificate, ips []net.IP) (*tls.Config, error) {
	privkey, err := cert.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("new private key: %w", err)
	}

	peerCert, err := cert.NewIPCert(ca.Leaf, ca.PrivateKey, privkey.Public(), ips...)
	if err != nil {
		return nil, fmt.Errorf("create IP cert: %w", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca.Leaf)

	return &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{peerCert},
				PrivateKey:  privkey,
			},
		},
		RootCAs: pool,
	}, nil
}

func findLocalIP() (net.IP, error) {
	slog.Info("trying to find a local IP")
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("get interface addresses: %w", err)
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				slog.Info("found local IP", "ip", ipnet.IP.String())
				return ipnet.IP, nil
			}
		}
	}
	return nil, fmt.Errorf("could not find a suitable local IP")
}

func caCert() (tls.Certificate, error) {
	caCert, err := tls.LoadX509KeyPair("ca.crt", "ca-key.pem")
	if err == nil {
		return caCert, nil
	}

	ca, caPrivateKey, err := cert.NewCA()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("new CA cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	if err := os.WriteFile("ca.crt", certPEM, 0o666); err != nil {
		return tls.Certificate{}, fmt.Errorf("write CA cert: %w", err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(caPrivateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal ca private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})
	if err := os.WriteFile("ca-key.pem", keyPEM, 0o666); err != nil {
		return tls.Certificate{}, fmt.Errorf("write ca key: %w", err)
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}
