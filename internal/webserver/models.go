package webserver

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go/http3"
)

type indexData struct {
	Errors []error
	Values url.Values
}

type hostFormData struct {
	Games  []GameEntry
	Errors []error
	Values url.Values
}

type hostRunningData struct {
	InviteCode  auth.Key
	GameAddress string
}

type peerFormData struct {
	Errors []error
	Values url.Values
}

type peerConnectedData struct {
	ListenAddress string
}

// sharedConfig holds the global configuration fields shared between host and peer modes.
// These are set on the index page before choosing a mode.
type sharedConfig struct {
	RegistarURL *url.URL
	Secret      string
	CACert      *x509.Certificate
	Port        int // port to listen on, 0 = auto-assign
}

func (cfg *sharedConfig) UnmarshalHTTP(r *http.Request) []error {
	if err := r.ParseMultipartForm(1 << 20); err != nil { // 1 MB max
		return []error{fmt.Errorf("invalid form data: %w", err)}
	}

	var errs []error

	// CA cert (optional file upload).
	file, _, err := r.FormFile("ca-cert")
	if err != nil {
		if !errors.Is(err, http.ErrMissingFile) {
			errs = append(errs, fmt.Errorf("failed to read CA cert file: %w", err))
		}
	} else {
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to read CA cert file: %w", err))
		} else {
			cfg.CACert, err = parseCACert(data)
			if err != nil {
				errs = append(errs, fmt.Errorf("invalid CA cert: %w", err))
			}
		}
	}

	// Registar URL (required).
	raw := r.FormValue("registar-url")
	if raw == "" {
		errs = append(errs, errors.New("URL is required"))
	} else {
		u, err := url.Parse(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid URL: %w", err))
		} else if u.Scheme != "http" && u.Scheme != "https" {
			errs = append(errs, errors.New("URL must start with http:// or https://"))
		} else {
			cfg.RegistarURL = u
		}
	}

	cfg.Secret = r.FormValue("secret")

	// optional, 0 = auto-assign
	if portStr := r.FormValue("port"); portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 || p > 65535 {
			errs = append(errs, errors.New("invalid port number"))
		} else {
			cfg.Port = p
		}
	}

	return errs
}

func (cfg *sharedConfig) tlsConfig() *tls.Config {
	if cfg.CACert == nil {
		return &tls.Config{
			NextProtos: []string{http3.NextProtoH3},
		}
	}

	pool := x509.NewCertPool()
	pool.AddCert(cfg.CACert)

	return &tls.Config{
		RootCAs:    pool,
		NextProtos: []string{http3.NextProtoH3}, // important, otherwise http3 client won't work
	}
}

func parseCACert(data []byte) (*x509.Certificate, error) {
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

type hostConfig struct {
	GamePort int
	GameKey  auth.Key
}

func (cfg *hostConfig) UnmarshalHTTP(r *http.Request) []error {
	if err := r.ParseForm(); err != nil {
		return []error{fmt.Errorf("invalid form data: %w", err)}
	}

	var errs []error

	// Game port (required). If "custom", read from custom-game-port.
	portStr := r.FormValue("game-port")
	if portStr == "custom" {
		portStr = r.FormValue("custom-game-port")
	}
	if portStr == "" {
		errs = append(errs, errors.New("port is required"))
	} else {
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 || p > 65535 {
			errs = append(errs, errors.New("invalid port number"))
		} else {
			cfg.GamePort = p
		}
	}

	// Game key (optional, generate if empty).
	if key := r.FormValue("game-key"); key != "" {
		k, err := auth.ParseKey(key)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid game key: %w", err))
		} else {
			cfg.GameKey = k
		}
	} else {
		cfg.GameKey = auth.NewKey()
	}

	return errs
}

type peerConfig struct {
	InviteCode     auth.Key
	GameListenPort int
}

func (cfg *peerConfig) UnmarshalHTTP(r *http.Request) []error {
	if err := r.ParseForm(); err != nil {
		return []error{fmt.Errorf("invalid form data: %w", err)}
	}

	var errs []error

	// Invite code (required).
	if code := r.FormValue("invite-code"); code != "" {
		k, err := auth.ParseKey(code)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid invite code: %w", err))
		} else {
			cfg.InviteCode = k
		}
	} else {
		errs = append(errs, errors.New("invite code is required"))
	}

	if portStr := r.FormValue("game-listen-port"); portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p <= 0 || p > 65535 {
			errs = append(errs, errors.New("invalid port number"))
		} else {
			cfg.GameListenPort = p
		}
	} else {
		cfg.GameListenPort = 0 // auto-assign
	}

	return errs
}
