package httpplatform

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
)

// RedirectHTTPS redirects all HTTP requests to HTTPS specified by tlsAddr.
func RedirectHTTPS(port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := targetHost(port, r.Host)

		targetURL := &url.URL{Scheme: "https", Host: host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}

		http.Redirect(w, r, targetURL.String(), http.StatusMovedPermanently)
	})
}

func targetHost(redirectPort int, host string) string {
	reqHost, _, _ := net.SplitHostPort(host) // r.Host is just host without port
	if reqHost == "" {
		reqHost = host
	}

	if redirectPort == 443 {
		return reqHost
	}
	return net.JoinHostPort(reqHost, strconv.Itoa(redirectPort))
}
