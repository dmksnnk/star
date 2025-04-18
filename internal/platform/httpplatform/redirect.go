package httpplatform

import (
	"net"
	"net/http"
	"net/url"
)

// RedirectHTTPS redirects all HTTP requests to HTTPS specified by tlsAddr.
func RedirectHTTPS(tlsAddr string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := targetHost(tlsAddr, r.Host)

		targetUrl := &url.URL{Scheme: "https", Host: host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}

		http.Redirect(w, r, targetUrl.String(), http.StatusMovedPermanently)
	})
}

func targetHost(tlsAddr string, host string) string {
	reqHost, _, _ := net.SplitHostPort(host) // r.Host is just host without port
	if reqHost == "" {
		reqHost = host
	}

	if tlsAddr == "" {
		return reqHost
	}

	target, port, _ := net.SplitHostPort(tlsAddr)
	if target == "" {
		if port == "443" {
			return reqHost
		}
		return net.JoinHostPort(reqHost, port)
	}

	if port == "443" { // known tls port
		return target
	}

	return tlsAddr
}
