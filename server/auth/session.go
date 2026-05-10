// Package auth — session.go manages the admin session cookie.
//
// Two modes:
//   - Redis present  → opaque random token stored at MH::sh:session:{token}
//   - Redis absent   → signed JWT stored directly as the cookie value
//
// Both modes use the same cookie name so the browser experience is identical.
package auth

import (
	"net/http"
	"os"

	"crypto/rand"
	"encoding/hex"

	"github.com/0xdps/mesahub-core/cache"
	"github.com/0xdps/mesahub-core/config"
)

// IssueSession creates a new admin session and writes the cookie into w.
func IssueSession(w http.ResponseWriter, r *http.Request, cfg *config.Config, c cache.Client) error {
	var cookieVal string

	if c.Available() {
		// Redis mode: random opaque token, session data stored server-side.
		token, err := newRandomHex(32)
		if err != nil {
			return err
		}
		if err := c.SetSession(r.Context(), token, cache.SessionValue{Role: "admin"}, sessionTTL); err != nil {
			return err
		}
		cookieVal = token
	} else {
		// JWT mode: signed token stored in the cookie itself.
		tok, err := IssueJWT(cfg.SessionSecret, "admin")
		if err != nil {
			return err
		}
		cookieVal = tok
	}

	// SECURE_COOKIES=true must be set explicitly in production Go deployments.
	// NODE_ENV is a Node.js convention and is not reliable in a Go process.
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(cfg),
		Value:    cookieVal,
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   os.Getenv("SECURE_COOKIES") == "true",
		Path:     "/",
	})
	return nil
}

// ValidateSession reads the session cookie and returns true if the session is
// valid. Returns (false, nil) when no cookie is present.
func ValidateSession(r *http.Request, cfg *config.Config, c cache.Client) (bool, error) {
	cookie, err := r.Cookie(CookieName(cfg))
	if err != nil {
		return false, nil
	}

	if c.Available() {
		sv, err := c.GetSession(r.Context(), cookie.Value)
		if err != nil {
			return false, err
		}
		return sv != nil, nil
	}

	_, err = ValidateJWT(cfg.SessionSecret, cookie.Value)
	return err == nil, nil
}

// DestroySession deletes the Redis session entry (if present) and clears the
// cookie in the browser.
func DestroySession(w http.ResponseWriter, r *http.Request, cfg *config.Config, c cache.Client) {
	if cookie, err := r.Cookie(CookieName(cfg)); err == nil && c.Available() {
		_ = c.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(cfg),
		Value:    "",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	})
}

// newRandomHex returns n random bytes as a lowercase hex string.
func newRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
