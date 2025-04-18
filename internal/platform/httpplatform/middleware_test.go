package httpplatform_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
)

func TestValidate(t *testing.T) {
	secret := []byte("secret")
	key := auth.NewKey()
	token := auth.NewToken(key, secret)

	handler := httpplatform.Authenticate(secret, httpplatform.TokenFromPathValue("token"))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotKey, ok := auth.KeyFromContext(r.Context())
			if !ok {
				t.Fatal("key not found in context")
			}

			if want, got := key, gotKey; want != got {
				t.Errorf("want key %s, got %s", want, got)
			}
		}),
	)

	mux := http.NewServeMux()
	mux.Handle("/{token}", handler)

	t.Run("valid token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+token.String(), http.NoBody)

		mux.ServeHTTP(rec, req)

		if want, got := http.StatusOK, rec.Code; want != got {
			t.Errorf("want status OK, got %d", rec.Code)
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/invalid", http.NoBody)

		mux.ServeHTTP(rec, req)

		if want, got := http.StatusUnauthorized, rec.Code; want != got {
			t.Errorf("want status OK, got %d", rec.Code)
		}
	})
}

func TestAllowHosts(t *testing.T) {
	tests := map[string]struct {
		allowed    []string
		host       string
		wantStatus int
	}{
		"allowed host": {
			allowed:    []string{"example.com"},
			host:       "example.com",
			wantStatus: http.StatusOK,
		},
		"allowed host with port": {
			allowed:    []string{"example.com"},
			host:       "example.com:1234",
			wantStatus: http.StatusOK,
		},
		"forbidden host": {
			allowed:    []string{"example.com"},
			host:       "example.org",
			wantStatus: http.StatusForbidden,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host, http.NoBody)
			handler := httpplatform.AllowHosts(tt.allowed)(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
			)

			handler.ServeHTTP(rec, req)

			if tt.wantStatus != rec.Code {
				t.Errorf("want status: %d, got %d", tt.wantStatus, rec.Code)
			}
		})
	}
}
