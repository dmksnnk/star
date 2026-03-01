package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/dmksnnk/star/internal/forwarder"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/webserver"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx, close := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer close()

	if len(os.Args) < 2 {
		runWeb(ctx)
		return
	}

	var cfg commandConfig
	if err := cfg.Parse(os.Args[1:]); err != nil {
		abort(cfg.FS, err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	tlsConfig, err := loadTLSConfig(cfg.CaCert)
	if err != nil {
		abort(cfg.FS, fmt.Errorf("load TLS config: %w", err))
		os.Exit(1)
	}

	// suppress quic-go's warning
	os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")

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

	logger.Debug("game registered", "key", gameKey.String())
	fmt.Printf("\n⭐ Invite Code: %s\n\n", gameKey.String())

	gameAddr := &net.UDPAddr{Port: hostCfg.GamePort}
	if err := host.AcceptAndLink(ctx, gameAddr); err != nil {
		logger.Error("run host", "error", err)
		os.Exit(1)
	}
}

func runPeer(ctx context.Context, cfg commandConfig, peerCfg peerConfig, logger *slog.Logger, tlsConfig *tls.Config) {
	token := auth.NewToken(peerCfg.Key, []byte(cfg.Secret))

	fwdCfg := forwarder.PeerConfig{
		Logger:         logger.With(slog.String("component", "peer")),
		TLSConfig:      tlsConfig,
		ListenAddr:     &net.UDPAddr{Port: cfg.Port},
		GameListenPort: peerCfg.GameListenPort,
	}

	peer, err := fwdCfg.Join(ctx, cfg.Registar.URL, token)
	if err != nil {
		logger.Error("register peer", "error", err)
		os.Exit(1)
	}
	defer peer.Close()

	logger.Debug("peer listening", "addr", peer.UDPAddr().String())
	fmt.Printf("\n⭐ Connect your game to: %s\n\n", peer.UDPAddr().String())

	if err := peer.AcceptAndLink(ctx); err != nil {
		logger.Error("accept and link", "error", err)
		os.Exit(1)
	}
}

func loadTLSConfig(caCertPath string) (*tls.Config, error) {
	if caCertPath == "" {
		return &tls.Config{
			NextProtos: []string{http3.NextProtoH3},
		}, nil
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

func runWeb(ctx context.Context) {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	logLevel := slog.LevelInfo
	if logLevelStr := os.Getenv("LOG_LEVEL"); logLevelStr != "" {
		_ = logLevel.UnmarshalText([]byte(logLevelStr))
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))

	srv, err := webserver.New()
	if err != nil {
		logger.Error("create web server", "error", err)
		os.Exit(1)
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error("listen", "error", err)
		os.Exit(1)
	}

	url := "http://" + l.Addr().String()
	if err := openBrowser(url); err != nil {
		logger.Warn("failed to open browser", "error", err)
	}
	fmt.Printf("Star is running at %s\nIf the browser didn't open automatically, copy and paste the link above into your browser.\n", url)

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: webserver.NewRouter(srv),
	}

	var eg errgroup.Group
	eg.Go(func() error {
		if err := httpSrv.Serve(l); err != http.ErrServerClosed {
			return fmt.Errorf("listen and serve: %w", err)
		}

		return nil
	})

	<-ctx.Done()

	logger.Debug("shutting down web server")

	srv.Stop()
	httpSrv.Close()

	if err := eg.Wait(); err != nil {
		logger.Error("web server error", "error", err)
		os.Exit(1)
	}
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
