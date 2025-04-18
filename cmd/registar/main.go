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
	http3platform "github.com/dmksnnk/star/internal/platform/http3"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/api"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/sync/errgroup"
)

type config struct {
	Secret           string     `env:"SECRET,required"`
	ListenAddress    string     `env:"LISTEN_ADDRESS" envDefault:":80"`
	ListenTLSAddress string     `env:"LISTEN_TLS_ADDRESS" envDefault:":443"`
	LogLevel         slog.Level `env:"LOG_LEVEL" envDefault:"INFO"`
	CertConfig       certConfig `envPrefix:"CERT_"`
}

type certConfig struct {
	SelfSigned bool     `env:"SELF_SIGNED" envDefault:"false"`
	Dir        string   `env:"DIR" envDefault:"certs"`
	Domains    []string `env:"DOMAINS" envDefault:"localhost"`
	IPAddress  []net.IP `env:"IP_ADDRESS"`
}

func main() {
	ctx, close := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer close()

	cfg := parseConfig()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))

	slog.LogAttrs(ctx, slog.LevelDebug, "load config", slog.Any("config", cfg))

	svc := registar.New()
	svc.NotifyPeerConnected(func(k auth.Key, peerID string) {
		slog.Info("peer connected",
			slog.String("key", k.String()),
			slog.String("peer_id", peerID),
		)
	})
	svc.NotifyPeerDisconnected(func(k auth.Key, peerID string, reason error) {
		slog.Info("peer disconnected",
			slog.String("key", k.String()),
			slog.String("peer_id", peerID),
			slog.String("error", errString(reason)),
		)
	})
	svc.NotifyHostDisconnected(func(k auth.Key, reason error) {
		slog.Info("host disconnected",
			slog.String("key", k.String()),
			slog.String("error", errString(reason)),
		)
	})

	tlsConf := newTLSConfig(cfg.CertConfig)

	mux := api.NewRouter(api.New(svc), []byte(cfg.Secret))
	addHealthCheck(mux)
	handler := httpplatform.Wrap(
		mux,
		httpplatform.LogRequests(slog.Default()),
		httpplatform.AllowHosts(cfg.CertConfig.Domains),
	)
	srvHTTP3 := api.NewServer(cfg.ListenTLSAddress, handler, tlsConf)

	advertiseHTTP3Mux := http.NewServeMux()
	addHealthCheck(advertiseHTTP3Mux)
	advertiseHTTP3Srv := http.Server{
		Addr: cfg.ListenTLSAddress,
		Handler: httpplatform.Wrap(
			advertiseHTTP3Mux,
			http3platform.AdvertiseHTTP3(srvHTTP3),
			httpplatform.LogRequests(slog.Default()),
			httpplatform.AllowHosts(cfg.CertConfig.Domains),
		),
		TLSConfig: tlsConf,
	}

	redirectHTTPSSrv := http.Server{
		Addr: cfg.ListenAddress,
		Handler: httpplatform.Wrap(
			httpplatform.RedirectHTTPS(cfg.ListenTLSAddress),
			httpplatform.LogRequests(slog.Default()),
			httpplatform.AllowHosts(cfg.CertConfig.Domains),
		),
	}

	eg, ctx := errgroup.WithContext(ctx)
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

	slog.Info("listening", "address", cfg.ListenAddress, "tls_address", cfg.ListenTLSAddress)

	<-ctx.Done()

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srvHTTP3.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown HTTP/3 server", "error", err)
	}

	if err := advertiseHTTP3Srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown HTTPS server", "error", err)
	}

	if err := redirectHTTPSSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown HTTP server", "error", err)
	}

	if err := eg.Wait(); err != nil {
		slog.Error("shutdown", "error", err)
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

func newTLSConfig(cfg certConfig) *tls.Config {
	if cfg.SelfSigned {
		tlsConf, err := selfSigned(cfg)
		if err != nil {
			slog.Error("create self signed cert", "error", err)
			os.Exit(1)
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

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}

	return err.Error()
}
