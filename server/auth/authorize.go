// Package auth — authorize.go contains DB-level access authorization helpers.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/0xdps/mesahub-core/cache"
	"github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/db"
)

// apiKeyTTL is how long a validated API key result is cached.
const apiKeyTTL = 15 * time.Minute

// AdminSessionHeader is set by AdminStamper after auth verification and is
// stripped from every incoming request by StripInternalHeaders so it cannot
// be forged externally.
const AdminSessionHeader = "X-Sqlite-Hub-Admin"

// ControlPlaneHeader is set by RequireControlPlane after verifying
// CONTROL_PLANE_SECRET. Stripped on ingress so it cannot be forged.
const ControlPlaneHeader = "X-Sqlite-Hub-Control"

// globalRegistry is the shared registry.db handle used to validate shk_ keys.
// Set once at startup via SetRegistry.
var globalRegistry *db.Registry

// SetRegistry provides the auth package with the shared Registry handle so
// that ValidateTemplateKey can look up shk_ API keys without a separate DB
// open on every cache miss. Must be called once before serving requests.
func SetRegistry(r *db.Registry) {
	globalRegistry = r
}

// UserKeyValidator, if non-nil, is called for Bearer tokens that are not shk_.
// The SaaS main.go wires this to its shs_ lookup at startup; template binary
// leaves it nil (all non-shk_ tokens are rejected with 401).
var UserKeyValidator func(ctx context.Context, c cache.Client, dataPath, token string) (*cache.APIKeyValue, bool)

// TimingSafeMatch compares a and b in constant time to prevent timing-oracle
// attacks. Returns false if either string is empty.
func TimingSafeMatch(a, b string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	bufA := make([]byte, maxLen)
	bufB := make([]byte, maxLen)
	copy(bufA, a)
	copy(bufB, b)
	return subtle.ConstantTimeCompare(bufA, bufB) == 1
}

// ValidateTemplateKey validates a shk_ API key against registry.db.
// It checks the cache first; on a miss it queries registry.db, stamps
// last_used_at, and populates the cache.
// Returns a populated APIKeyValue and true on success, nil and false otherwise.
func ValidateTemplateKey(ctx context.Context, c cache.Client, keyValue string) (*cache.APIKeyValue, bool) {
	sum := sha256.Sum256([]byte(keyValue))
	keyHash := hex.EncodeToString(sum[:])

	// ── Cache hit ─────────────────────────────────────────────────────────────
	if cached, err := c.GetAPIKey(ctx, keyHash); err == nil && cached != nil {
		return cached, true
	}

	// ── Cache miss: query registry.db ────────────────────────────────────────
	if globalRegistry == nil {
		return nil, false
	}
	rec, err := globalRegistry.GetAPIKeyByHash(keyHash)
	if err != nil || rec == nil || rec.Status != "active" {
		return nil, false
	}

	var scopes []string
	if err := json.Unmarshal([]byte(rec.Scopes), &scopes); err != nil {
		return nil, false
	}

	// Stamp last_used_at — best-effort.
	_ = globalRegistry.TouchAPIKey(rec.ID)

	kv := &cache.APIKeyValue{
		KeyID:  rec.ID,
		Owner:  rec.Owner,
		Scopes: scopes,
	}
	_ = c.SetAPIKey(ctx, keyHash, *kv, apiKeyTTL)
	return kv, true
}

// hasPermission reports whether any of the given scopes grants the requested
// operation on the specified resource.
//
// resourceType is "db" or "bucket"; slug is the template-internal identifier;
// op is "read" or "write". A ":w" scope satisfies both read and write.
func hasPermission(scopes []string, resourceType, slug, op string) bool {
	for _, s := range scopes {
		parts := strings.SplitN(s, ":", 3)
		switch len(parts) {
		case 2:
			// "all:r" or "all:w"
			if parts[0] == "all" {
				if parts[1] == "w" {
					return true
				}
				if parts[1] == "r" && op == "read" {
					return true
				}
			}
		case 3:
			rtype, target, level := parts[0], parts[1], parts[2]
			if rtype != resourceType {
				continue
			}
			// Wildcard or exact slug match
			if target != "*" && target != slug {
				continue
			}
			if level == "w" {
				return true // write implies read
			}
			if level == "r" && op == "read" {
				return true
			}
		}
	}
	return false
}

// AuthorizeDB enforces the per-DB access rules and discards the validated key
// value. Use AuthorizeDBWithKey when the caller needs the key value.
func AuthorizeDB(r *http.Request, cfg *config.Config, c cache.Client, record *db.DBRecord) (int, string) {
	code, msg, _ := AuthorizeDBWithKey(r, cfg, c, record)
	return code, msg
}

// AuthorizeDBWithKey enforces per-DB access rules and returns the validated
// *cache.APIKeyValue (nil for admin requests or on failure).
//
// Resolution order:
//  1. X-Sqlite-Hub-Admin: 1  → allowed (admin session set by AdminStamper)
//  2. Bearer shk_*           → ValidateTemplateKey (registry.db api_keys)
//  3. Bearer other           → UserKeyValidator hook (SaaS wires shs_ here)
//  4. No valid credential    → 401
func AuthorizeDBWithKey(r *http.Request, cfg *config.Config, c cache.Client, record *db.DBRecord) (int, string, *cache.APIKeyValue) {
	if r.Header.Get(AdminSessionHeader) == "1" {
		return 0, "", nil
	}
	if record.Status != "active" {
		return http.StatusServiceUnavailable, "This database is inactive", nil
	}

	bearer := extractBearer(r)
	if bearer == "" {
		return http.StatusUnauthorized, "Unauthorized", nil
	}

	kv, ok := ValidateTemplateKey(r.Context(), c, bearer)
	if !ok {
		return http.StatusUnauthorized, "Unauthorized", nil
	}
	// Owner check: admin keys (owner="admin") have full access; user-scoped
	// keys must match the resource owner.
	if kv.Owner != "admin" && kv.Owner != record.Owner {
		return http.StatusUnauthorized, "Unauthorized", nil
	}
	op := "read"
	if strings.Contains(r.URL.Path, "/exec") {
		op = "write"
	}
	if !hasPermission(kv.Scopes, "db", record.Name, op) {
		return http.StatusForbidden, "This API key does not have access to this database", nil
	}
	return 0, "", kv
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}

// AuthorizeBucket enforces access control for bucket file operations.
// Admin bearer tokens always pass. shk_ keys are accepted when they carry a
// scope that grants access to the bucket (e.g. "bucket:{slug}:w").
// op is "read" or "write".
func AuthorizeBucket(r *http.Request, cfg *config.Config, c cache.Client, record *db.BucketRecord, op string) (int, string) {
	if r.Header.Get(AdminSessionHeader) == "1" {
		return 0, ""
	}
	if record.Status != "active" {
		return http.StatusServiceUnavailable, "This bucket is inactive"
	}

	bearer := extractBearer(r)
	if bearer == "" {
		return http.StatusUnauthorized, "Unauthorized"
	}

	kv, ok := ValidateTemplateKey(r.Context(), c, bearer)
	if !ok {
		return http.StatusUnauthorized, "Unauthorized"
	}
	if !hasPermission(kv.Scopes, "bucket", record.Slug, op) {
		return http.StatusForbidden, "This API key does not have access to this bucket"
	}
	return 0, ""
}
