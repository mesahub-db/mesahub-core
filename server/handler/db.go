// Package handler — db.go handles /api/db and /api/db/:name routes.
package handler

import (
	"net/http"
	"os"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/0xdps/mesahub-core/auth"
	"github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/db"
	"github.com/0xdps/mesahub-core/sysutil"
)

var nameRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// DBHandler holds dependencies for the /api/db route group.
type DBHandler struct {
	cfg      *config.Config
	pool     *db.Pool
	registry *db.Registry
}

// NewDBHandler creates a DBHandler.
func NewDBHandler(cfg *config.Config, pool *db.Pool, registry *db.Registry) *DBHandler {
	return &DBHandler{cfg: cfg, pool: pool, registry: registry}
}

// ── /api/db ───────────────────────────────────────────────────────────────────

// ListDBs handles GET /api/db (admin only).
func (h *DBHandler) ListDBs(w http.ResponseWriter, r *http.Request) {
	rows, err := h.registry.ListDatabases()
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]any, len(rows))
	for i, rec := range rows {
		out[i] = h.withStats(&rec)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateDB handles POST /api/db (admin only).
// Body: { name: "user-given label", slug: "template-filename", owner, source?, instance_id?, description? }
// A UUID id is generated server-side.
func (h *DBHandler) CreateDB(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string  `json:"name"`
		Slug        string  `json:"slug"`
		Owner       string  `json:"owner"`
		Source      string  `json:"source"`
		InstanceID  *string `json:"instance_id"`
		Description string  `json:"description"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Slug == "" {
		body.Slug = body.Name
	}
	if body.Name == "" || body.Owner == "" {
		ErrorJSON(w, http.StatusBadRequest, "name and owner are required")
		return
	}
	if body.Source == "" {
		body.Source = "admin"
	}
	if !nameRegex.MatchString(body.Slug) {
		ErrorJSON(w, http.StatusBadRequest, "slug must match ^[A-Za-z0-9_-]+$")
		return
	}

	pct := sysutil.VolumeUsagePct(h.cfg.DataPath)
	if pct >= h.cfg.MaxVolumeUsagePct {
		ErrorJSON(w, http.StatusInsufficientStorage,
			"volume usage "+itoa(pct)+"% exceeds limit of "+itoa(h.cfg.MaxVolumeUsagePct)+"%")
		return
	}

	existing, _ := h.registry.GetDatabase(body.Slug)
	if existing != nil {
		ErrorJSON(w, http.StatusConflict, "Database already exists")
		return
	}

	// Touch the SQLite file with WAL mode via the pool (creates if absent).
	if _, err := h.pool.Get(body.Slug); err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to create database file: "+err.Error())
		return
	}

	var desc *string
	if body.Description != "" {
		d := body.Description
		desc = &d
	}

	id := uuid.New().String()
	record, err := h.registry.InsertDatabase(id, body.Name, body.Slug, body.Owner, body.Source, body.InstanceID, desc)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Info().Str("slug", body.Slug).Str("owner", body.Owner).Msg("[db] created database")

	writeJSON(w, http.StatusCreated, h.withStats(record))
}

// ── /api/db/:name ─────────────────────────────────────────────────────────────

// GetDB handles GET /api/db/:name where :name is the slug (template filename).
func (h *DBHandler) GetDB(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	record, err := h.registry.GetDatabase(slug)
	if err != nil || record == nil {
		ErrorJSON(w, http.StatusNotFound, "Not found")
		return
	}
	writeJSON(w, http.StatusOK, h.withStats(record))
}

// PatchDB handles PATCH /api/db/:name (admin only), where :name is the slug.
func (h *DBHandler) PatchDB(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	record, err := h.registry.GetDatabase(slug)
	if err != nil || record == nil {
		ErrorJSON(w, http.StatusNotFound, "Not found")
		return
	}

	var body map[string]any
	if !decodeJSON(w, r, &body) {
		return
	}
	action, _ := body["action"].(string)

	switch action {
	case "set_status":
		status, _ := body["status"].(string)
		if status != "active" && status != "inactive" {
			ErrorJSON(w, http.StatusBadRequest, "status must be 'active' or 'inactive'")
			return
		}
		if err := h.registry.SetDatabaseStatus(slug, status); err != nil {
			ErrorJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Info().Str("slug", slug).Str("status", status).Msg("[db] status set")
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": status})

	case "reset_db":
		dbPath := sysutil.DBPath(h.cfg.DataPath, slug)
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			ErrorJSON(w, http.StatusNotFound, "Database file not found")
			return
		}
		if err := h.pool.Remove(slug); err != nil {
			log.Warn().Err(err).Str("slug", slug).Msg("[db] pool remove error on reset")
		}
		// Wipe the database file and WAL/SHM sidecars so the DB is truly empty.
		_ = os.Remove(dbPath)
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
		log.Info().Str("slug", slug).Msg("[db] database reset — file deleted")
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	default:
		ErrorJSON(w, http.StatusBadRequest, "Invalid action")
	}
}

// DeleteDB handles DELETE /api/db/:name (admin only, soft delete), where :name is the slug.
func (h *DBHandler) DeleteDB(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	record, err := h.registry.GetDatabase(slug)
	if err != nil || record == nil {
		ErrorJSON(w, http.StatusNotFound, "Not found")
		return
	}
	if err := h.registry.SoftDeleteDatabase(h.pool, slug); err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Info().Str("slug", slug).Msg("[db] soft deleted")
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// ── /api/db/deleted ───────────────────────────────────────────────────────────

// ListDeletedDBs handles GET /api/db/deleted (admin only).
func (h *DBHandler) ListDeletedDBs(w http.ResponseWriter, r *http.Request) {
	rows, err := h.registry.ListDeletedDatabases()
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]any, len(rows))
	for i, rec := range rows {
		out[i] = h.withStats(&rec)
	}
	writeJSON(w, http.StatusOK, out)
}

// DeletedDBAction handles POST /api/db/deleted/:name (restore or hard_delete).
// :name is the renamed slug (e.g. "mesahub_user_mydb-1234567890").
func (h *DBHandler) DeletedDBAction(w http.ResponseWriter, r *http.Request) {
	deletedSlug := chi.URLParam(r, "name")
	record, err := h.registry.GetDatabase(deletedSlug)
	if err != nil || record == nil || record.Status != "deleted" {
		ErrorJSON(w, http.StatusNotFound, "Deleted database not found")
		return
	}

	var body map[string]any
	if !decodeJSON(w, r, &body) {
		return
	}
	action, _ := body["action"].(string)

	switch action {
	case "restore":
		if !record.OriginalSlug.Valid {
			ErrorJSON(w, http.StatusBadRequest, "Missing original slug")
			return
		}
		existing, _ := h.registry.GetDatabase(record.OriginalSlug.String)
		if existing != nil {
			ErrorJSON(w, http.StatusConflict,
				"A database with slug \""+record.OriginalSlug.String+"\" already exists")
			return
		}
		restored, err := h.registry.RestoreDatabase(h.pool, deletedSlug)
		if err != nil {
			ErrorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info().Str("from", deletedSlug).Str("to", restored.Slug).Msg("[db] restored")
		writeJSON(w, http.StatusOK, h.withStats(restored))

	case "hard_delete":
		if err := h.registry.HardDeleteDatabase(deletedSlug); err != nil {
			ErrorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info().Str("slug", deletedSlug).Msg("[db] hard deleted")
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	default:
		ErrorJSON(w, http.StatusBadRequest, "Invalid action. Use 'restore' or 'hard_delete'")
	}
}

// RequireAdmin is a convenience alias so routes in main.go avoid a second import.
func RequireAdmin(next http.Handler) http.Handler {
	return auth.RequireAdmin(next)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (h *DBHandler) withStats(rec *db.DBRecord) map[string]any {
	// File on disk uses the slug (template-internal filename).
	filePath := sysutil.DBPath(h.cfg.DataPath, rec.Slug)
	_, statErr := os.Stat(filePath)
	return map[string]any{
		"id":            rec.ID,
		"name":          rec.Name,
		"slug":          rec.Slug,
		"owner":         rec.Owner,
		"description":   nullStr(rec.Description),
		"instance_id":   nullStr(rec.InstanceID),
		"created_at":    rec.CreatedAt,
		"updated_at":    nullStr(rec.UpdatedAt),
		"status":        rec.Status,
		"original_slug": nullStr(rec.OriginalSlug),
		"deleted_at":    nullStr(rec.DeletedAt),
		"file_path":     filePath,
		"size_bytes":    sysutil.FileSizeBytes(filePath),
		"exists":        statErr == nil,
	}
}
