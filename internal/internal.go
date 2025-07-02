package internal

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// NewDialer creates a new HTTP/3 dialer with optional CA certificate.
func NewDialer(caCert string) (*http3platform.HTTP3Dialer, error) {
	dialer := &http3platform.HTTP3Dialer{
		TLSConfig: &tls.Config{
			NextProtos: []string{http3.NextProtoH3},
		},
		QUICConfig: &quic.Config{
			EnableDatagrams:         true,
			DisablePathMTUDiscovery: true,
		},
	}
	if caCert != "" {
		data, err := os.ReadFile(caCert)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		if err != nil {
			return nil, fmt.Errorf("load CA certificate: %w", err)
		}
		dialer.TLSConfig.RootCAs = x509.NewCertPool()
		dialer.TLSConfig.RootCAs.AppendCertsFromPEM(data)
	}

	return dialer, nil
}
