package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/discovery"
	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/relay"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/sync/errgroup"
)

type config struct {
	Secret     string     `env:"SECRET,required"`
	LogLevel   slog.Level `env:"LOG_LEVEL" envDefault:"INFO"`
	Listen     listenConfig
	CertConfig certConfig
}

type listenConfig struct {
	// HTTP is the listen address on for HTTP connections to redirect to HTTPS.
	HTTP string `env:"LISTEN_HTTP" envDefault:":80"`
	// TLS is the listen address for HTTPS and HTTP/3 connections.
	TLS string `env:"LISTEN_TLS" envDefault:":443"`
	// Discovery is the listen address for the address discovery service.
	Discovery string `env:"LISTEN_DISCOVERY" envDefault:":8000"`
	// Relay is the listen address for the UDP relay service.
	Relay string `env:"LISTEN_RELAY" envDefault:":8001"`
}

type certConfig struct {
	// SelfSigned indicates whether to use a self-signed certificate.
	SelfSigned bool     `env:"CERT_SELF_SIGNED" envDefault:"false"`
	Dir        string   `env:"CERT_DIR" envDefault:"certs"`
	Domains    []string `env:"CERT_DOMAINS" envDefault:"localhost"`
	IPAddress  []net.IP `env:"CERT_IP_ADDRESS"`
}

func main() {
	ctx, close := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer close()

	cfg := parseConfig()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	logger.DebugContext(ctx, "load config", slog.Any("config", cfg))

	eg, ctx := errgroup.WithContext(ctx)

	discoverySrv := discovery.NewServer()

	relayUDPAddr := resolveUDPAddr(cfg.Listen.Relay)
	relaySrv := relay.NewUDPRelay(relay.WithLogger(logger.With("component", "relay")))

	registarSvc := registar.NewRegistar2(relayUDPAddr.AddrPort(), relaySrv)
	rootCA, err := registar.NewRootCA()
	if err != nil {
		abort("create root CA", err)
	}

	tlsConf := newTLSConfig(cfg.CertConfig)

	caAuthority := registar.NewAuthority(rootCA)
	api := registar.NewAPI(registarSvc, caAuthority)
	router := registar.NewRouter(api, []byte(cfg.Secret))
	addHealthCheck(router)
	handler := httpplatform.Wrap(
		router,
		httpplatform.LogRequests(logger.With(slog.String("component", "registar"))),
		httpplatform.AllowHosts(cfg.CertConfig.Domains),
	)
	srvHTTP3 := registar.NewServer(cfg.Listen.TLS, handler, tlsConf)

	advertiseHTTP3Mux := http.NewServeMux()
	addHealthCheck(advertiseHTTP3Mux)
	advertiseHTTP3Srv := http.Server{
		Addr: cfg.Listen.TLS,
		Handler: httpplatform.Wrap(
			advertiseHTTP3Mux,
			http3platform.AdvertiseHTTP3(srvHTTP3),
			httpplatform.LogRequests(slog.Default()),
			httpplatform.AllowHosts(cfg.CertConfig.Domains),
		),
		TLSConfig: tlsConf,
	}

	redirectHTTPSSrv := http.Server{
		Addr: cfg.Listen.HTTP,
		Handler: httpplatform.Wrap(
			httpplatform.RedirectHTTPS(cfg.Listen.TLS),
			httpplatform.LogRequests(logger.With(slog.String("component", "redirect_https"))),
			httpplatform.AllowHosts(cfg.CertConfig.Domains),
		),
	}

	eg.Go(func() error {
		if err := discoverySrv.Listen(cfg.Listen.Discovery); err != nil {
			return fmt.Errorf("listen discovery: %w", err)
		}

		return nil
	})

	eg.Go(func() error {
		if err := relaySrv.ListenUDP(relayUDPAddr); err != nil {
			return fmt.Errorf("listen relay: %w", err)
		}

		return nil
	})

	eg.Go(func() error {
		if err := srvHTTP3.ListenAndServe(); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}

			return fmt.Errorf("listen and serve HTTP/3: %w", err)
		}

		return nil
	})

	eg.Go(func() error {
		if err := advertiseHTTP3Srv.ListenAndServeTLS("", ""); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}

			return fmt.Errorf("listen and serve TLS HTTP: %w", err)
		}

		return nil
	})

	eg.Go(func() error {
		if err := redirectHTTPSSrv.ListenAndServe(); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}

			return fmt.Errorf("listen and serve HTTP: %w", err)
		}

		return nil
	})

	logger.Info("listening",
		slog.Group("address",
			slog.String("http", cfg.Listen.HTTP),
			slog.String("discovery", cfg.Listen.Discovery),
			slog.String("relay", cfg.Listen.Relay),
			slog.String("tls", cfg.Listen.TLS),
		),
	)

	<-ctx.Done()

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := discoverySrv.Close(); err != nil {
		logger.Error("shutdown discovery server", "error", err)
	}

	if err := relaySrv.Close(); err != nil {
		logger.Error("shutdown relay server", "error", err)
	}

	if err := srvHTTP3.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown HTTP/3 server", "error", err)
	}

	if err := advertiseHTTP3Srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown HTTPS server", "error", err)
	}

	if err := redirectHTTPSSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown HTTP server", "error", err)
	}

	if err := eg.Wait(); err != nil {
		logger.Error("shutdown", "error", err)
		os.Exit(1)
	}
}

func parseConfig() config {
	var cfg config
	if err := env.Parse(&cfg); err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}
	cfg.Secret = strings.TrimSpace(cfg.Secret) // to remove trailing newlines

	return cfg
}

func resolveUDPAddr(address string) *net.UDPAddr {
	udpAddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		abort(fmt.Sprintf("resolve UDP address %q", address), err)
	}

	return udpAddr
}

func newTLSConfig(cfg certConfig) *tls.Config {
	if cfg.SelfSigned {
		tlsConf, err := selfSigned(cfg)
		if err != nil {
			abort("create self signed cert", err)
		}

		return tlsConf
	}

	mgr := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cfg.Dir),
		HostPolicy: autocert.HostWhitelist(cfg.Domains...),
	}
	return mgr.TLSConfig()
}

func selfSigned(cfg certConfig) (*tls.Config, error) {
	ca, caPrivateKey, err := cert.NewCA()
	if err != nil {
		return nil, fmt.Errorf("create CA: %w", err)
	}

	if err := writeCACert(ca); err != nil {
		return nil, fmt.Errorf("write CA certificate: %w", err)
	}

	srvPrivkey, err := cert.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("create server private key: %w", err)
	}

	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"Star"},
		},
		DNSNames:    cfg.Domains,
		IPAddresses: cfg.IPAddress,
	}
	reqDer, err := x509.CreateCertificateRequest(rand.Reader, template, srvPrivkey)
	if err != nil {
		return nil, fmt.Errorf("create certificate request: %w", err)
	}

	req, err := x509.ParseCertificateRequest(reqDer)
	if err != nil {
		return nil, fmt.Errorf("parse certificate request: %w", err)
	}

	srvCert, err := cert.NewCert(ca, caPrivateKey, req)
	if err != nil {
		return nil, fmt.Errorf("create server cert: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: [][]byte{srvCert},
				PrivateKey:  srvPrivkey,
			},
		},
		NextProtos: []string{http3.NextProtoH3, "h2", "http/1.1"},
	}, nil
}

func writeCACert(cert *x509.Certificate) error {
	f, err := os.Create("ca.crt")
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
		return fmt.Errorf("encode certificate: %w", err)
	}

	return nil
}

func addHealthCheck(mux *http.ServeMux) {
	mux.HandleFunc("/-/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func abort(msg string, err error) {
	slog.Error(msg, "error", err)
	os.Exit(1)
}
