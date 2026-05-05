// Package auth — middleware.go provides HTTP middleware for session stamping,
// admin-only route protection, and CORS.
package auth

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/0xdps/mesahub-core/cache"
	"github.com/0xdps/mesahub-core/config"
)

// AdminStamper is a global middleware that stamps X-MesaHub-Admin: 1 on
// the request when the caller presents valid credentials:
//
//   - "Authorization: Bearer {ADMIN_TOKEN}" — server-to-server access
//   - A valid session cookie                — browser admin session
//
// The header is stripped from incoming requests by StripInternalHeaders
// before this middleware runs, so the value cannot be forged.
func AdminStamper(cfg *config.Config, c cache.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Server-to-server: Bearer ADMIN_TOKEN (timing-safe)
			if cfg.AdminToken != "" {
				if raw := r.Header.Get("Authorization"); strings.HasPrefix(raw, "Bearer ") {
					if TimingSafeMatch(strings.TrimPrefix(raw, "Bearer "), cfg.AdminToken) {
						r.Header.Set(AdminSessionHeader, "1")
						next.ServeHTTP(w, r)
						return
					}
				}
			}
			// Browser: session cookie
			if ok, _ := ValidateSession(r, cfg, c); ok {
				r.Header.Set(AdminSessionHeader, "1")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAdmin rejects requests that do not carry the admin-session header.
// It must be used downstream of AdminStamper.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(AdminSessionHeader) != "1" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CORS returns a middleware that sets CORS headers based on cfg.CORSOrigins.
// It handles OPTIONS pre-flight inline, returning 204 No Content.
func CORS(cfg *config.Config) func(http.Handler) http.Handler {
	allowed := parseCORSOrigins(cfg.CORSOrigins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && len(allowed) > 0 {
				if ao := resolveOrigin(origin, allowed); ao != "" {
					w.Header().Set("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Origin", ao)
					w.Header().Set("Access-Control-Allow-Methods",
						"GET,POST,PUT,PATCH,DELETE,OPTIONS,HEAD")
					w.Header().Set("Access-Control-Allow-Headers",
						"Authorization, Content-Type, X-Requested-With")
					if ao != "*" {
						w.Header().Set("Access-Control-Allow-Credentials", "true")
					}
				}
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func resolveOrigin(origin string, allowed []string) string {
	for _, o := range allowed {
		if o == "*" {
			return "*"
		}
		if o == origin {
			return origin
		}
	}
	return ""
}

func parseCORSOrigins(raw string) []string {
	var result []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			result = append(result, o)
		}
	}
	return result
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
