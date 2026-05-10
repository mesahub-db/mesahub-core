// Package auth — jwt.go contains the JWT primitives used for cookie-based
// sessions when Redis is not available.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/0xdps/mesahub-core/config"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// sessionTTL is the lifetime of a newly issued session.
	sessionTTL = 24 * time.Hour
)

// CookieName returns the admin session cookie name derived from cfg.CookiePrefix.
// With the default prefix "sqlitedbhub" this is "sqlitedbhub_session".
func CookieName(cfg *config.Config) string {
	return cfg.CookiePrefix + "_session"
}

// Claims is the JWT payload stored inside a cookie-based admin session.
type Claims struct {
	jwt.RegisteredClaims
	Role string `json:"role"`
}

// IssueJWT creates a signed HS256 JWT valid for sessionTTL.
func IssueJWT(secret, role string) (string, error) {
	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(sessionTTL)),
		},
		Role: role,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ValidateJWT parses and validates a signed JWT, returning its claims.
// Returns an error if the token is expired, malformed, or uses the wrong algorithm.
func ValidateJWT(secret, tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
