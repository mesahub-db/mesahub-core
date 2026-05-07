// Package handler — apikeys.go handles /api/apikeys routes (admin-managed shk_ keys).
package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/0xdps/mesahub-core/db"
)

// APIKeysHandler holds dependencies for the /api/apikeys route group.
type APIKeysHandler struct {
	registry *db.Registry
}

// NewAPIKeysHandler creates an APIKeysHandler.
func NewAPIKeysHandler(registry *db.Registry) *APIKeysHandler {
	return &APIKeysHandler{registry: registry}
}

// CreateAPIKey handles POST /api/apikeys (admin only).
// Returns the raw key value once — it is never stored or retrievable again.
func (h *APIKeysHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string   `json:"name"`
		Owner     string   `json:"owner"`
		KeyType   string   `json:"key_type"`
		Scopes    []string `json:"scopes"`
		ExpiresAt *string  `json:"expires_at"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == "" {
		ErrorJSON(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.Owner == "" {
		body.Owner = "admin"
	}
	if body.KeyType == "" {
		body.KeyType = "admin"
	}

	raw, err := generateAPIKey()
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "key generation failed")
		return
	}

	hash := sha256Hex(raw)
	id := uuid.Must(uuid.NewV7()).String()

	scopesJSON := `["all:w"]`
	if len(body.Scopes) > 0 {
		if b, merr := json.Marshal(body.Scopes); merr == nil {
			scopesJSON = string(b)
		}
	}

	rec, err := h.registry.InsertAPIKey(id, body.Name, hash, scopesJSON, body.Owner, body.KeyType, body.ExpiresAt)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         rec.ID,
		"name":       rec.Name,
		"key":        raw,
		"scopes":     rec.Scopes,
		"expires_at": nullableString(rec.ExpiresAt),
		"created_at": rec.CreatedAt,
	})
}

// ListAPIKeys handles GET /api/apikeys (admin only).
func (h *APIKeysHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.registry.ListAPIKeys()
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, len(keys))
	for i, k := range keys {
		out[i] = map[string]any{
			"id":           k.ID,
			"name":         k.Name,
			"scopes":       k.Scopes,
			"expires_at":   nullableString(k.ExpiresAt),
			"last_used_at": nullableString(k.LastUsedAt),
			"created_at":   k.CreatedAt,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// RevokeAPIKey handles DELETE /api/apikeys/{id} (admin only).
func (h *APIKeysHandler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		ErrorJSON(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.registry.RevokeAPIKey(id); err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "shk_" + hex.EncodeToString(b), nil
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func nullableString(ns sql.NullString) any {
	if !ns.Valid {
		return nil
	}
	return ns.String
}
