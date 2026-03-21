package httpplatform

import (
	"net"
	"net/http"
	"net/netip"
)

// RequestIP extracts the client's IP address from the request.
// As we are not running behind a proxy, we can trust RemoteAddr.
func RequestIP(r *http.Request) netip.Addr {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	addr, _ := netip.ParseAddr(ip)

	return addr
}
