package httpplatform

import (
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/dmksnnk/star/internal/registar/auth"
)

type Middleware = func(http.Handler) http.Handler

func LogRequests(logger *slog.Logger) Middleware {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			handler.ServeHTTP(w, r)

			var attrs []slog.Attr
			key, ok := auth.KeyFromContext(r.Context())
			if ok {
				attrs = append(attrs, slog.String("game_key", key.String()))
			}

			attrs = append(attrs, slog.Group("request",
				slog.String("method", r.Method),
				slog.String("proto", r.Proto),
				slog.String("host", r.Host),
				slog.String("remote", r.RemoteAddr),
				slog.String("path", r.URL.Path),
				slog.String("query", r.URL.RawQuery),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.Group("headers", headers(r.Header)...),
			))

			logger.LogAttrs(r.Context(), slog.LevelDebug, "request", attrs...)
		})
	}
}

// AllowHosts allows requests only for the specified hosts.
func AllowHosts(hosts []string) Middleware {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, _ := net.SplitHostPort(r.Host)
			if host == "" { // r.Host is just host without port
				host = r.Host
			}

			if slices.Contains(hosts, host) {
				handler.ServeHTTP(w, r)
				return
			}

			http.Error(w, "forbidden", http.StatusForbidden)
		})
	}
}

// Authenticate validates the token in the request and adds it to context.
// Responds with 401 if the token is invalid.
func Authenticate(secret []byte, tokener func(*http.Request) string) Middleware {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokeStr := tokener(r)
			token, err := auth.ParseToken(tokeStr)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}

			if !auth.VerifyToken(secret, token) {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}

			r = r.WithContext(auth.ContextWithKey(r.Context(), token.Key()))
			handler.ServeHTTP(w, r)
		})
	}
}

func TokenFromPathValue(name string) func(*http.Request) string {
	return func(r *http.Request) string {
		return r.PathValue(name)
	}
}

func TokenFromHeader(name string) func(*http.Request) string {
	return func(r *http.Request) string {
		return r.Header.Get(name)
	}
}

func headers(hs http.Header) []any {
	attrs := make([]any, 0, len(hs))
	for k, vs := range hs {
		attrs = append(attrs, slog.String(k, vs[0]))
	}

	return attrs
}

// Wrap handler with middlewares.
func Wrap(handler http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		mw := mws[i]
		if mw != nil {
			handler = mw(handler)
		}
	}

	return handler
}
