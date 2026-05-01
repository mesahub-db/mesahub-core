// Package filetoken implements short-lived HMAC-signed file access tokens.
// Mirrors file-access-token.ts exactly.
//
// Token format: base64url(json_payload) + "." + base64url(HMAC-SHA256)
// The signing secret comes from env FILE_TOKEN_SIGNING_SECRET.
package filetoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultTTLSeconds = 60 * 60 * 24 * 30  // 30 days
	maxTTLSeconds     = 60 * 60 * 24 * 365 // 1 year
)

// Payload is the decoded content of a file access token.
type Payload struct {
	TokenID string `json:"tokenId"`
	DBName  string `json:"dbName"`
	Scope   string `json:"scope"`
	// "files:read"  — download authorisation (shortlink)
	// "files:write" — upload authorisation (PUT /{ns}/upload)
	ExpiresAt int64 `json:"expiresAt"`

	// Write-scope constraints — zero value means unconstrained.
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	FolderPath  string `json:"folderPath,omitempty"`
}

// CreateResult holds the values returned to the caller when a token is issued.
type CreateResult struct {
	TokenID   string
	Token     string
	ExpiresAt time.Time
	ExpiresIn int
	Scope     string
}

// signingSecret reads FILE_TOKEN_SIGNING_SECRET from the environment.
func signingSecret() ([]byte, error) {
	s := os.Getenv("FILE_TOKEN_SIGNING_SECRET")
	if s == "" {
		return nil, errors.New("FILE_TOKEN_SIGNING_SECRET is not set")
	}
	return []byte(s), nil
}

// Create issues a new file access token for dbName.
// expiresInSeconds ≤ 0 means use the default TTL.
func Create(dbName string, expiresInSeconds int) (CreateResult, error) {
	secret, err := signingSecret()
	if err != nil {
		return CreateResult{}, err
	}

	ttl := expiresInSeconds
	if ttl <= 0 {
		ttl = defaultTTLSeconds
	}
	ttl = int(math.Min(math.Max(float64(ttl), 60), float64(maxTTLSeconds)))

	tokenID := mustUUID()
	expiresAt := time.Now().Unix() + int64(ttl)

	p := Payload{
		TokenID:   tokenID,
		DBName:    dbName,
		Scope:     "files:read",
		ExpiresAt: expiresAt,
	}
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		return CreateResult{}, err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	sig := computeSig(secret, payloadB64)
	token := payloadB64 + "." + sig

	return CreateResult{
		TokenID:   tokenID,
		Token:     token,
		ExpiresAt: time.Unix(expiresAt, 0),
		ExpiresIn: ttl,
		Scope:     "files:read",
	}, nil
}

// Verify parses and validates a token string. Returns the Payload or an error.
func Verify(token string) (*Payload, error) {
	secret, err := signingSecret()
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed token")
	}
	payloadB64, providedSig := parts[0], parts[1]

	expectedSig := computeSig(secret, payloadB64)
	if !timingSafeEqual(providedSig, expectedSig) {
		return nil, errors.New("invalid signature")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	var p Payload
	if err := json.Unmarshal(payloadJSON, &p); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	if p.DBName == "" || p.TokenID == "" || p.Scope == "" {
		return nil, errors.New("token missing required fields")
	}
	if p.ExpiresAt < time.Now().Unix() {
		return nil, errors.New("token expired")
	}
	return &p, nil
}

// FromRequest extracts a token string from the "token" query parameter or
// "Authorization: Bearer ..." header. Returns "" if not present.
func FromRequest(r *http.Request) string {
	if q := r.URL.Query().Get("token"); q != "" {
		return q
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// ValidateFromRequest validates a token from the request for a specific dbName
// and additionally checks the revocation list via isRevoked.
// isRevoked(tokenID) must return true when the token has been revoked — the
// caller is responsible for querying the registry. On any revocation-check
// error, favour security: treat as revoked (return false).
func ValidateFromRequest(r *http.Request, dbName string, isRevoked func(tokenID string) bool) bool {
	raw := FromRequest(r)
	if raw == "" {
		return false
	}
	p, err := Verify(raw)
	if err != nil {
		return false
	}
	if p.Scope != "files:read" {
		return false
	}
	if p.DBName != dbName {
		return false
	}
	if isRevoked(p.TokenID) {
		return false
	}
	return true
}

// CreateUpload issues a files:write token for the presigned-upload flow.
// The filename, contentType, and folderPath are embedded so the upload handler
// can enforce that the received file matches what was authorised.
func CreateUpload(dbName, filename, contentType, folderPath string, expiresInSeconds int) (CreateResult, error) {
	secret, err := signingSecret()
	if err != nil {
		return CreateResult{}, err
	}

	ttl := expiresInSeconds
	if ttl <= 0 {
		ttl = defaultTTLSeconds
	}
	ttl = int(math.Min(math.Max(float64(ttl), 60), float64(maxTTLSeconds)))

	tokenID := mustUUID()
	expiresAt := time.Now().Unix() + int64(ttl)

	p := Payload{
		TokenID:     tokenID,
		DBName:      dbName,
		Scope:       "files:write",
		ExpiresAt:   expiresAt,
		Filename:    filename,
		ContentType: contentType,
		FolderPath:  folderPath,
	}
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		return CreateResult{}, err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	sig := computeSig(secret, payloadB64)
	token := payloadB64 + "." + sig

	return CreateResult{
		TokenID:   tokenID,
		Token:     token,
		ExpiresAt: time.Unix(expiresAt, 0),
		ExpiresIn: ttl,
		Scope:     "files:write",
	}, nil
}

// computeSig returns base64url(HMAC-SHA256(secret, payloadB64)).
func computeSig(secret []byte, payloadB64 string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payloadB64))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func timingSafeEqual(a, b string) bool {
	ab := []byte(a)
	bb := []byte(b)
	if len(ab) != len(bb) {
		// Pad to same length so ConstantTimeCompare always runs.
		maxLen := len(ab)
		if len(bb) > maxLen {
			maxLen = len(bb)
		}
		padA := make([]byte, maxLen)
		padB := make([]byte, maxLen)
		copy(padA, ab)
		copy(padB, bb)
		return subtle.ConstantTimeCompare(padA, padB) == 1
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}

func mustUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail on a healthy system. If it does, panicking
		// is safer than silently returning all-zero UUIDs, which would cause all
		// tokens to share the same token_id and make revocation of one revoke all.
		panic("filetoken: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
