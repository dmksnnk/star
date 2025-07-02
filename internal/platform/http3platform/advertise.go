package http3platform

import (
	"net/http"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/quic-go/quic-go/http3"
)

// AdvertiseHTTP3 advertises HTTP/3 support on HTTP/1/2 server.
func AdvertiseHTTP3(srv *http3.Server) httpplatform.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor < 3 {
				if err := srv.SetQUICHeaders(w.Header()); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}

				next.ServeHTTP(w, r)
			}
		})
	}
}
