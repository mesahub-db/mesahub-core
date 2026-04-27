// Package handler — bucket_admin.go handles admin-level /api/buckets routes.
// These are backed by registry.db (local volume buckets, shk_ + admin auth).
// SaaS bucket file operations (shs_ auth, S3/R2 backend) live in the saas module.
package handler

import (
	"database/sql"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/db"
	"github.com/0xdps/mesahub-core/files"
)

// BucketAdminHandler handles admin-level /api/buckets routes.
type BucketAdminHandler struct {
	cfg      *config.Config
	registry *db.Registry
	storage  *files.Storage
}

// NewBucketAdminHandler creates a BucketAdminHandler.
func NewBucketAdminHandler(cfg *config.Config, registry *db.Registry, storage *files.Storage) *BucketAdminHandler {
	return &BucketAdminHandler{cfg: cfg, registry: registry, storage: storage}
}

// ListBuckets handles GET /api/buckets (admin only).
func (h *BucketAdminHandler) ListBuckets(w http.ResponseWriter, r *http.Request) {
	buckets, err := h.registry.ListBuckets()
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, len(buckets))
	for i, b := range buckets {
		out[i] = bucketJSON(&b)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateBucket handles POST /api/buckets (admin only).
// Body: { name: "user-given label", slug: "template-filename", owner?, source?, instance_id?, description? }
// A UUID id is generated server-side.
func (h *BucketAdminHandler) CreateBucket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string  `json:"name"`
		Slug        string  `json:"slug"`
		Owner       string  `json:"owner"`
		Source      string  `json:"source"`
		InstanceID  *string `json:"instance_id"`
		Description *string `json:"description"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == "" || body.Slug == "" {
		ErrorJSON(w, http.StatusBadRequest, "name and slug are required")
		return
	}
	if body.Owner == "" {
		body.Owner = "admin"
	}
	if body.Source == "" {
		body.Source = "admin"
	}
	if !nameRegex.MatchString(body.Slug) {
		ErrorJSON(w, http.StatusBadRequest, "slug may only contain letters, digits, hyphens, and underscores")
		return
	}

	id := uuid.New().String()
	rec, err := h.registry.InsertBucket(id, body.Name, body.Slug, body.Owner, body.Source, body.InstanceID, body.Description)
	if err != nil {
		ErrorJSON(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, bucketJSON(rec))
}

// DeleteBucket handles DELETE /api/buckets/{name} (admin only), where name is the slug.
func (h *BucketAdminHandler) DeleteBucket(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	if slug == "" {
		ErrorJSON(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := h.registry.DeleteBucket(slug); err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func bucketJSON(b *db.BucketRecord) map[string]any {
	return map[string]any{
		"id":          b.ID,
		"name":        b.Name,
		"slug":        b.Slug,
		"description": nullableString(b.Description),
		"status":      b.Status,
		"size_bytes":  b.SizeBytes,
		"created_at":  b.CreatedAt,
		"updated_at":  nullableString(b.UpdatedAt),
		"deleted_at":  nullableString(b.DeletedAt),
	}
}

// ── Bucket file routes ────────────────────────────────────────────────────────

// ListFiles handles GET /api/buckets/{name}/files (admin only).
func (h *BucketAdminHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}
	q := r.URL.Query()
	limit := queryInt(q, "limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := queryInt(q, "offset", 0)
	folderPrefix := q.Get("folder_prefix")
	sort := q.Get("sort")
	order := q.Get("order")

	result, err := h.storage.List(slug, limit, offset, folderPrefix, sort, order)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// UploadFile handles POST /api/buckets/{name}/files (admin only).
func (h *BucketAdminHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}

	if err := r.ParseMultipartForm(h.cfg.FileMaxSizeBytes + (1 << 20)); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, h.cfg.FileMaxSizeBytes+1))
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to read file")
		return
	}
	if int64(len(data)) > h.cfg.FileMaxSizeBytes {
		ErrorJSON(w, http.StatusRequestEntityTooLarge, files.ErrFileTooLarge.Error())
		return
	}

	filename := r.FormValue("filename")
	if filename == "" {
		filename = fileHeader.Filename
	}
	contentType := r.FormValue("content_type")
	if contentType == "" {
		contentType = fileHeader.Header.Get("Content-Type")
	}
	folderPath := r.FormValue("folder_path")
	conflictMode := files.ConflictMode(r.FormValue("conflict_mode"))
	metaStr := r.FormValue("metadata")
	expiresInStr := r.FormValue("expires_in")

	var expiresAt sql.NullString
	if expiresInStr != "" {
		if secs, err := strconv.Atoi(expiresInStr); err == nil && secs > 0 {
			expiresAt = sql.NullString{
				String: time.Now().UTC().Add(time.Duration(secs) * time.Second).Format(time.RFC3339),
				Valid:  true,
			}
		}
	}

	in := files.UploadInput{
		DBName:       slug,
		FolderPath:   folderPath,
		Filename:     filename,
		ContentType:  contentType,
		Data:         data,
		ConflictMode: conflictMode,
		ExpiresAt:    expiresAt,
		Metadata:     sql.NullString{String: metaStr, Valid: metaStr != ""},
	}

	result, err := h.storage.Upload(in)
	if err != nil {
		code := http.StatusInternalServerError
		switch err {
		case files.ErrFileTooLarge:
			code = http.StatusRequestEntityTooLarge
		case files.ErrConflict:
			code = http.StatusConflict
		case files.ErrTooManyFiles, files.ErrStorageQuota:
			code = http.StatusInsufficientStorage
		case files.ErrMimeNotAllowed, files.ErrMimeMismatch:
			code = http.StatusUnsupportedMediaType
		}
		ErrorJSON(w, code, err.Error())
		return
	}

	stored, _ := h.storage.GetByID(result.ID)
	if stored == nil {
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":         result.ID,
			"size_bytes": result.SizeBytes,
		})
		return
	}
	writeJSON(w, http.StatusCreated, bucketFileRecord(stored, slug))
}

// DownloadFile handles GET /api/buckets/{name}/files/{id} (admin only).
func (h *BucketAdminHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	id := chi.URLParam(r, "id")

	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}

	stored, err := h.storage.GetByID(id)
	if err != nil || stored == nil || stored.DBName != slug {
		ErrorJSON(w, http.StatusNotFound, "File not found")
		return
	}

	ct := stored.ContentType.String
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(stored.SizeBytes, 10))
	w.Header().Set("Content-Disposition",
		"inline; filename=\""+sanitizeHeaderFilename(stored.Filename)+`"`)
	w.Header().Set("X-Sendfile", "/"+filepath.Base(stored.StoragePath))
	w.WriteHeader(http.StatusOK)
}

// DeleteFile handles DELETE /api/buckets/{name}/files/{id} (admin only).
func (h *BucketAdminHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	id := chi.URLParam(r, "id")

	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}

	if err := h.storage.DeleteByID(id, slug); err != nil {
		if strings.Contains(err.Error(), "not found") {
			ErrorJSON(w, http.StatusNotFound, err.Error())
			return
		}
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func bucketFileRecord(f *files.StoredFile, slug string) map[string]any {
	url := "/api/buckets/" + slug + "/files/" + f.ID
	ct := ""
	if f.ContentType.Valid {
		ct = f.ContentType.String
	}
	return map[string]any{
		"id":           f.ID,
		"filename":     f.Filename,
		"folder_path":  f.FolderPath,
		"size_bytes":   f.SizeBytes,
		"content_type": ct,
		"url":          url,
		"uploaded_at":  f.UploadedAt,
		"expires_at":   nullStr(f.ExpiresAt),
		"metadata":     nullStr(f.Metadata),
	}
}
