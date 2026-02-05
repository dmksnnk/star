package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmksnnk/star/internal/forwarder"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go/http3"
)

func main() {
	ctx, close := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer close()

	var cfg commandConfig
	if err := cfg.Parse(os.Args[1:]); err != nil {
		abort(cfg.FS, err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	tlsConfig, err := loadTLSConfig(cfg.CaCert)
	if err != nil {
		abort(cfg.FS, fmt.Errorf("load TLS config: %w", err))
		os.Exit(1)
	}

	switch cfg.Command {
	case "host":
		var hostCfg hostConfig
		if err := hostCfg.Parse(cfg.FS.Args()[1:]); err != nil {
			abort(hostCfg.FS, err)
		}
		runHost(ctx, cfg, hostCfg, logger, tlsConfig)
	case "peer":
		var peerCfg peerConfig
		if err := peerCfg.Parse(cfg.FS.Args()[1:]); err != nil {
			abort(peerCfg.FS, err)
		}
		runPeer(ctx, cfg, peerCfg, logger, tlsConfig)
	default:
		abort(cfg.FS, fmt.Errorf("unknown command: %s", cfg.Command))
	}

	slog.Info("bye")
}

func runHost(ctx context.Context, cfg commandConfig, hostCfg hostConfig, logger *slog.Logger, tlsConfig *tls.Config) {
	gameKey := hostCfg.GameKey
	if gameKey == (auth.Key{}) {
		gameKey = auth.NewKey()
	}

	token := auth.NewToken(gameKey, []byte(cfg.Secret))

	fwdCfg := forwarder.HostConfig{
		Logger:     logger.With(slog.String("component", "host")),
		TLSConfig:  tlsConfig,
		ListenAddr: &net.UDPAddr{Port: cfg.Port},
		ErrHandlers: []func(error){
			func(err error) {
				logger.Error("host forwarder error", "error", err)
			},
		},
	}

	host, err := fwdCfg.Register(ctx, cfg.Registar.URL, token)
	if err != nil {
		slog.Error("register host", "error", err)
		os.Exit(1)
	}
	defer host.Close()

	logger.Info("game registered", "key", gameKey.String())

	gameAddr := &net.UDPAddr{Port: hostCfg.GamePort}
	if err := host.Run(ctx, gameAddr); err != nil {
		logger.Error("run host", "error", err)
		os.Exit(1)
	}
}

func runPeer(ctx context.Context, cfg commandConfig, peerCfg peerConfig, logger *slog.Logger, tlsConfig *tls.Config) {
	token := auth.NewToken(peerCfg.Key, []byte(cfg.Secret))

	fwdCfg := forwarder.PeerConfig{
		Logger:             logger.With(slog.String("component", "peer")),
		TLSConfig:          tlsConfig,
		RegistarListenAddr: &net.UDPAddr{Port: cfg.Port},
		GameListenPort:     peerCfg.GameListenPort,
	}

	peer, err := fwdCfg.Join(ctx, cfg.Registar.URL, token)
	if err != nil {
		logger.Error("register peer", "error", err)
		os.Exit(1)
	}
	defer peer.Close()

	logger.Info("peer listening", "addr", peer.UDPAddr().String())

	if err := peer.AcceptAndLink(ctx); err != nil {
		logger.Error("accept and link", "error", err)
		os.Exit(1)
	}
}

func loadTLSConfig(caCertPath string) (*tls.Config, error) {
	if caCertPath == "" {
		return &tls.Config{}, nil
	}

	caCert, err := loadCACert(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("load CA certificate: %w", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &tls.Config{
		RootCAs:    pool,
		NextProtos: []string{http3.NextProtoH3}, // important, otherwise http3 client won't work
	}, nil
}

func loadCACert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("invalid CA certificate")
	}

	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	return caCert, nil
}
