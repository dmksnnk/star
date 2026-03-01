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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/sync/errgroup"
)

type config struct {
	SecretFromFile string     `env:"SECRET_FILE,file"` // if set, takes precedence over Secret
	Secret         string     `env:"SECRET"`
	LogLevel       slog.Level `env:"LOG_LEVEL" envDefault:"INFO"`
	HTTP           httpConfig
	HTTPS          httpsConfig
	Cert           certConfig
}

type httpConfig struct {
	// Listen is the listen address for HTTP connections to redirect to HTTPS.
	Listen string `env:"HTTP_LISTEN" envDefault:":80"`
}

type httpsConfig struct {
	// Listen is the listen address for HTTPS and HTTP/3 connections.
	Listen string `env:"HTTPS_LISTEN" envDefault:":443"`
	// AdvertiseHTTP3Port is the port to advertise for HTTP/3 support in Alt-Svc header.
	// This can be different, especially when running in a container with port mapping.
	AdvertiseHTTP3Port int `env:"HTTPS_ADVERTISE_HTTP3_PORT" envDefault:"443"`
	// RedirectPort is the port to redirect HTTP requests to HTTPS in the redirect server.
	// This can be different, especially when running in a container with port mapping.
	RedirectPort int `env:"HTTPS_REDIRECT_PORT" envDefault:"443"`
}

type certConfig struct {
	// SelfSigned indicates whether to use a self-signed certificate.
	SelfSigned bool `env:"CERT_SELF_SIGNED" envDefault:"false"`
	// Dir to store certificates.
	Dir string `env:"CERT_DIR" envDefault:"certs"`
	// Domains to request or generate certificates for.
	Domains []string `env:"CERT_DOMAINS" envDefault:"localhost"`
}

func main() {
	ctx, close := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer close()

	cfg := parseConfig()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	logger.DebugContext(ctx, "load config", slog.Any("config", cfg))

	eg, ctx := errgroup.WithContext(ctx)

	rootCA, err := registar.NewRootCA()
	if err != nil {
		abort("create root CA", err)
	}

	caAuthority := registar.NewAuthority(rootCA)

	tlsConf, acmeMgr := newTLSConfig(cfg.Cert)

	srvHTTP3 := registar.NewServer(caAuthority)
	router := registar.NewRouter(srvHTTP3, []byte(cfg.Secret))
	addHealthCheck(router)
	handler := httpplatform.Wrap(
		router,
		httpplatform.LogRequests(logger.With(slog.String("component", "registar"))),
		httpplatform.AllowHosts(cfg.Cert.Domains),
	)
	srvHTTP3.H3.Handler = handler
	srvHTTP3.H3.Addr = cfg.HTTPS.Listen
	srvHTTP3.H3.TLSConfig = tlsConf.Clone()
	srvHTTP3.H3.TLSConfig.NextProtos = []string{http3.NextProtoH3}
	srvHTTP3.Logger = logger.With(slog.String("component", "registar"))

	advertiseHTTP3Mux := http.NewServeMux()
	addHealthCheck(advertiseHTTP3Mux)
	advertiseHTTP3Srv := http.Server{
		Addr: cfg.HTTPS.Listen,
		Handler: httpplatform.Wrap(
			advertiseHTTP3Mux,
			http3platform.AdvertiseHTTP3(cfg.HTTPS.AdvertiseHTTP3Port),
			httpplatform.LogRequests(logger.With(slog.String("component", "advertise_http3"))),
			httpplatform.AllowHosts(cfg.Cert.Domains),
		),
		TLSConfig: tlsConf.Clone(),
	}

	redirectHandler := httpplatform.Wrap(
		httpplatform.RedirectHTTPS(cfg.HTTPS.RedirectPort),
		httpplatform.LogRequests(logger.With(slog.String("component", "redirect_https"))),
		httpplatform.AllowHosts(cfg.Cert.Domains),
	)
	if acmeMgr != nil {
		redirectHandler = acmeMgr.HTTPHandler(redirectHandler)
	}
	redirectHTTPSSrv := http.Server{
		Addr:    cfg.HTTP.Listen,
		Handler: redirectHandler,
	}

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
			slog.String("http", cfg.HTTP.Listen),
			slog.String("https", cfg.HTTPS.Listen),
		),
	)

	<-ctx.Done()

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srvHTTP3.Close(); err != nil {
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
		abort("parse config", err)
	}
	cfg.SecretFromFile = strings.TrimSpace(cfg.SecretFromFile) // to remove trailing newlines
	cfg.Secret = strings.TrimSpace(cfg.Secret)                 // to remove trailing newlines
	if cfg.SecretFromFile == "" && cfg.Secret == "" {
		abort("secret is required", errors.New("either SECRET_FILE or SECRET environment variable must be set"))
	}
	if cfg.SecretFromFile != "" {
		cfg.Secret = cfg.SecretFromFile
	}

	return cfg
}

func newTLSConfig(cfg certConfig) (*tls.Config, *autocert.Manager) {
	if cfg.SelfSigned {
		tlsConf, err := selfSigned(cfg)
		if err != nil {
			abort("create self signed cert", err)
		}

		return tlsConf, nil
	}

	mgr := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cfg.Dir),
		HostPolicy: autocert.HostWhitelist(cfg.Domains...),
	}
	return mgr.TLSConfig(), mgr
}

func selfSigned(cfg certConfig) (*tls.Config, error) {
	ca, caPrivateKey, err := cert.NewCA()
	if err != nil {
		return nil, fmt.Errorf("create CA: %w", err)
	}

	if err := writeCACert(cfg.Dir, ca); err != nil {
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
		DNSNames: cfg.Domains,
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

func writeCACert(dir string, cert *x509.Certificate) error {
	f, err := os.Create(filepath.Join(dir, "ca.crt"))
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
