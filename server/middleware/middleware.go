// Package middleware provides shared HTTP middleware for the Go service.
package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
)

// Logger returns a chi-compatible zerolog request-logging middleware.
// Health check requests to /api/health are silently skipped to avoid
// polluting the console with noise from Docker / load-balancer probes.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/api/health" {
			return
		}
		log.Debug().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", ww.Status()).
			Int("bytes", ww.BytesWritten()).
			Dur("latency", time.Since(start)).
			Str("remote", r.RemoteAddr).
			Msg("request")
	})
}

// StripInternalHeaders removes headers that the Go service uses internally
// so that external clients cannot spoof them.
func StripInternalHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-MesaHub-Admin")
		r.Header.Del("X-MesaHub-User-Id")
		r.Header.Del("X-Internal-Request")
		r.Header.Del("X-MesaHub-Control")
		next.ServeHTTP(w, r)
	})
}
