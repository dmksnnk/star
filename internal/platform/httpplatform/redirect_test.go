package httpplatform_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
)

func TestRedirectHTTPS(t *testing.T) {
	tests := map[string]struct {
		redirect string
		target   string
		want     string
	}{
		"to host:port from host:port": {
			redirect: "other-address:8083",
			target:   "http://some-address:8000/path?key=value",
			want:     "https://other-address:8083/path?key=value",
		},
		"to host:port from host": {
			redirect: "other-address:8083",
			target:   "http://some-address/path?key=value",
			want:     "https://other-address:8083/path?key=value",
		},
		"to port from host:port": {
			redirect: ":8083",
			target:   "http://some-address:8080/path?key=value",
			want:     "https://some-address:8083/path?key=value",
		},
		"to port from host": {
			redirect: ":8083",
			target:   "http://some-address/path?key=value",
			want:     "https://some-address:8083/path?key=value",
		},
		"to tls port from host:port": {
			redirect: ":443",
			target:   "http://some-address:8080/path?key=value",
			want:     "https://some-address/path?key=value",
		},
		"to tls port from host": {
			redirect: ":443",
			target:   "http://some-address/path?key=value",
			want:     "https://some-address/path?key=value",
		},
		"to empty from host:port": {
			redirect: "",
			target:   "http://some-address:8080/path?key=value",
			want:     "https://some-address/path?key=value",
		},
		"to empty from host": {
			redirect: "",
			target:   "http://some-address/path?key=value",
			want:     "https://some-address/path?key=value",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			handler := httpplatform.RedirectHTTPS(tt.redirect)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)

			handler.ServeHTTP(rec, req)
			res := rec.Result()
			if res.StatusCode != 301 {
				t.Errorf("got status code %d, want 301", res.StatusCode)
			}
			if got := res.Header.Get("Location"); got != tt.want {
				t.Errorf("got Location header %q, want %q", got, tt.want)
			}
		})
	}
}
