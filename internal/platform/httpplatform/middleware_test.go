package httpplatform_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
)

func TestAuthenticate(t *testing.T) {
	t.Run("with secret", func(t *testing.T) {
		secret := []byte("secret")
		key := auth.NewKey()
		token := auth.NewToken(key, secret)

		handler := httpplatform.Authenticate(secret, httpplatform.TokenFromPathValue("token"))(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotKey, ok := auth.KeyFromContext(r.Context())
				if !ok {
					t.Fatal("key not found in context")
				}

				if gotKey != key {
					t.Errorf("want key %s, got %s", key, gotKey)
				}
			}),
		)

		mux := http.NewServeMux()
		mux.Handle("/{token}", handler)

		t.Run("valid token", func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/"+token.String(), http.NoBody)

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("want status OK, got %d", rec.Code)
			}
		})

		t.Run("invalid token", func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/invalid", http.NoBody)

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("want status Unauthorized, got %d", rec.Code)
			}
		})
	})

	t.Run("without secret", func(t *testing.T) {
		key := auth.NewKey()
		token := auth.NewToken(key, nil)

		handler := httpplatform.Authenticate(nil, httpplatform.TokenFromHeader("x-token"))(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotKey, ok := auth.KeyFromContext(r.Context())
				if !ok {
					t.Fatal("key not found in context")
				}

				if gotKey != key {
					t.Errorf("want key %s, got %s", key, gotKey)
				}
			}),
		)

		t.Run("valid token", func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req.Header.Set("x-token", token.String())

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("want status OK, got %d", rec.Code)
			}
		})

		t.Run("invalid token", func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req.Header.Set("x-token", "invalid")

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("want status Unauthorized, got %d", rec.Code)
			}
		})

		t.Run("missing token", func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("want status Unauthorized, got %d", rec.Code)
			}
		})
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

func TestRateLimit(t *testing.T) {
	t.Run("rate limits", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			limiter := httpplatform.RateLimit(time.Minute, 1, httpplatform.RequestIP)
			req := httptest.NewRequest(http.MethodGet, "http://example.com", http.NoBody)
			handler := limiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			// first request should pass
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("want status OK, got %d", rec.Code)
			}

			// second request should be rate limited
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("want status TooManyRequests, got %d", rec.Code)
			}

			if ra := rec.Header().Get("Retry-After"); ra != "60" {
				t.Fatalf("want Retry-After 60 seconds, got %s", ra)
			}

			time.Sleep(time.Minute)

			// after waiting, request should pass again
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("want status OK, got %d", rec.Code)
			}
		})
	})

	t.Run("separate limiters per IP", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			limiter := httpplatform.RateLimit(time.Minute, 1, httpplatform.RequestIP)
			handler := limiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			reqA := httptest.NewRequest(http.MethodGet, "http://example.com", http.NoBody)
			reqA.RemoteAddr = "192.0.2.1:1234"

			reqB := httptest.NewRequest(http.MethodGet, "http://example.com", http.NoBody)
			reqB.RemoteAddr = "192.0.2.2:1234"

			// exhaust IP A's limit
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, reqA)
			if rec.Code != http.StatusOK {
				t.Fatalf("want status OK, got %d", rec.Code)
			}

			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, reqA)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("want status TooManyRequests, got %d", rec.Code)
			}

			// IP B should be unaffected
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, reqB)
			if rec.Code != http.StatusOK {
				t.Fatalf("want status OK for different IP, got %d", rec.Code)
			}
		})
	})
}
