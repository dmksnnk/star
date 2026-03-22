package httpplatform

import (
	"compress/gzip"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/dmksnnk/star/internal/platform"
	"github.com/dmksnnk/star/internal/registar/auth"
)

type Middleware = func(http.Handler) http.Handler

func LogRequests(logger *slog.Logger) Middleware {
	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rw := newResponseWriterWithStatus(w)
			handler.ServeHTTP(rw, r)

			var attrs []slog.Attr
			key, ok := auth.KeyFromContext(r.Context())
			if ok {
				attrs = append(attrs, slog.String("game_key", key.String()))
			}

			attrs = append(attrs,
				slog.Group("request",
					slog.String("method", r.Method),
					slog.String("proto", r.Proto),
					slog.String("host", r.Host),
					slog.String("remote", r.RemoteAddr),
					slog.String("path", r.URL.Path),
					slog.String("query", r.URL.RawQuery),
					slog.Int64("duration_ms", time.Since(start).Milliseconds()),
					slog.GroupAttrs("headers", headers(r.Header)...),
				),
				slog.Group("response",
					slog.Int("status", rw.Status()),
					slog.GroupAttrs("headers", headers(rw.Header())...),
				),
			)

			logger.LogAttrs(r.Context(), slog.LevelDebug, "request", attrs...)
		})
	}
}

type responseWriterWithStatus struct {
	http.ResponseWriter
	status int
}

func newResponseWriterWithStatus(w http.ResponseWriter) *responseWriterWithStatus {
	return &responseWriterWithStatus{ResponseWriter: w}
}

func (w *responseWriterWithStatus) WriteHeader(statusCode int) {
	w.status = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseWriterWithStatus) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}

	return w.status
}

func (w *responseWriterWithStatus) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
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
// Responds with 401 if the token is invalid or missing.
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

func RateLimit(every time.Duration, burst int, requestIP func(*http.Request) netip.Addr) Middleware {
	ttl := every
	if burst > 1 {
		ttl = time.Duration(burst) * every
	}
	ipLimiterMap := platform.NewTTLMap[netip.Addr, *rate.Limiter](ttl)
	limit := rate.Every(every)

	return func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := requestIP(r)
			limiter := ipLimiterMap.GetOrSet(ip, func() *rate.Limiter {
				return rate.NewLimiter(limit, burst)
			})

			reservation := limiter.Reserve()
			if delay := reservation.Delay(); delay > 0 {
				reservation.Cancel()
				if delay < rate.InfDuration { // in case burst set to 0, Delay returns InfDuration
					ra := int(math.Ceil(delay.Seconds()))
					w.Header().Set("Retry-After", strconv.Itoa(ra))
				}
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

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

func headers(hs http.Header) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(hs))
	for k, vs := range hs {
		attrs = append(attrs, slog.String(k, vs[0]))
	}

	return attrs
}

// CacheStaticFiles sets Cache-Control headers for static file extensions.
func CacheStaticFiles(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Ext(r.URL.Path) {
		case ".css", ".js", ".svg", ".png":
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}

		next.ServeHTTP(w, r)
	})
}

// Gzip compresses responses for compressible file types when the client supports it.
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		switch filepath.Ext(r.URL.Path) {
		case "", ".html", ".css", ".js", ".json", ".xml", ".svg":
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")
			w.Header().Del("Content-Length")
			grw := newGzipResponseWriter(w)
			defer grw.Close()
			next.ServeHTTP(grw, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func newGzipResponseWriter(rw http.ResponseWriter) *gzipResponseWriter {
	return &gzipResponseWriter{
		ResponseWriter: rw,
		w:              gzip.NewWriter(rw),
	}
}

type gzipResponseWriter struct {
	http.ResponseWriter
	w *gzip.Writer
}

func (rw *gzipResponseWriter) Write(b []byte) (int, error) {
	return rw.w.Write(b)
}

func (rw *gzipResponseWriter) Close() error {
	return rw.w.Close()
}

func (rw *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Wrap handler with middlewares.
func Wrap(handler http.Handler, mws ...Middleware) http.Handler {
	for _, mw := range slices.Backward(mws) {
		if mw != nil {
			handler = mw(handler)
		}
	}

	return handler
}
