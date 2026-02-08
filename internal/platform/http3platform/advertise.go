package http3platform

import (
	"fmt"
	"net/http"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/quic-go/quic-go/http3"
)

// AdvertiseHTTP3 advertises HTTP/3 support on HTTP/1/2 server.
func AdvertiseHTTP3(port int) httpplatform.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor < 3 {
				w.Header().Set("Alt-Svc", fmt.Sprintf(`%s=":%d"; ma=2592000`, http3.NextProtoH3, port))

				next.ServeHTTP(w, r)
			}
		})
	}
}
