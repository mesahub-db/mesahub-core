// Package handler — bucket_admin.go handles /api/buckets routes.
// Bucket management (create/delete/list) requires admin.
// Bucket file operations (upload/download/presign) accept admin OR the
// bucket-specific shk_ API key returned at creation time.
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

	"github.com/0xdps/mesahub-core/auth"
	"github.com/0xdps/mesahub-core/cache"
	"github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/db"
	"github.com/0xdps/mesahub-core/files"
	"github.com/0xdps/mesahub-core/filetoken"
)

// BucketAdminHandler handles /api/buckets routes.
type BucketAdminHandler struct {
	cfg      *config.Config
	registry *db.Registry
	storage  files.BucketStorage
	cache    cache.Client
	// DefaultBackend is the storage backend used for new buckets when the
	// request body does not specify one. Defaults to "local" if empty.
	// Cloud server sets this based on which object-store backends are configured.
	DefaultBackend string
}

// NewBucketAdminHandler creates a BucketAdminHandler.
func NewBucketAdminHandler(cfg *config.Config, registry *db.Registry, storage files.BucketStorage, c cache.Client) *BucketAdminHandler {
	return &BucketAdminHandler{cfg: cfg, registry: registry, storage: storage, cache: c}
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
// Body: { name, slug, owner?, source?, instance_id?, description? }
// A UUID id is generated server-side. A dedicated shk_ API key is auto-generated
// and returned once in the response — it cannot be retrieved again.
func (h *BucketAdminHandler) CreateBucket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name           string  `json:"name"`
		Slug           string  `json:"slug"`
		Owner          string  `json:"owner"`
		Source         string  `json:"source"`
		InstanceID     *string `json:"instance_id"`
		Description    *string `json:"description"`
		StorageBackend string  `json:"storage_backend"` // optional: "local", "s3", "r2"
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

	// Determine the storage backend: request body > handler default > "local".
	backend := body.StorageBackend
	if backend == "" {
		backend = h.DefaultBackend
	}
	if backend == "" {
		backend = "local"
	}

	id := uuid.New().String()
	rec, rawKey, err := h.registry.InsertBucket(id, body.Name, body.Slug, body.Owner, body.Source, body.InstanceID, body.Description, backend)
	if err != nil {
		ErrorJSON(w, http.StatusConflict, err.Error())
		return
	}
	out := bucketJSON(rec)
	// Return the raw key once — caller must store it.
	out["api_key"] = rawKey
	writeJSON(w, http.StatusCreated, out)
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
		"id":              b.ID,
		"name":            b.Name,
		"slug":            b.Slug,
		"description":     nullableString(b.Description),
		"status":          b.Status,
		"size_bytes":      b.SizeBytes,
		"storage_backend": b.StorageBackend,
		"created_at":      b.CreatedAt,
		"updated_at":      nullableString(b.UpdatedAt),
		"deleted_at":      nullableString(b.DeletedAt),
	}
}

// ── Bucket file routes ────────────────────────────────────────────────────────

// ListFiles handles GET /api/buckets/{name}/files (admin or bucket key).
func (h *BucketAdminHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}
	if code, msg := auth.AuthorizeBucket(r, h.cfg, h.cache, rec, "read"); code != 0 {
		ErrorJSON(w, code, msg)
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

// UploadFile handles POST /api/buckets/{name}/files (admin or bucket key).
func (h *BucketAdminHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}
	if code, msg := auth.AuthorizeBucket(r, h.cfg, h.cache, rec, "write"); code != 0 {
		ErrorJSON(w, code, msg)
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

// DownloadFile handles GET /api/buckets/{name}/files/{id} (admin or bucket key).
func (h *BucketAdminHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	id := chi.URLParam(r, "id")

	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}
	if code, msg := auth.AuthorizeBucket(r, h.cfg, h.cache, rec, "read"); code != 0 {
		ErrorJSON(w, code, msg)
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

// DeleteFile handles DELETE /api/buckets/{name}/files/{id} (admin or bucket key).
func (h *BucketAdminHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	id := chi.URLParam(r, "id")

	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}
	if code, msg := auth.AuthorizeBucket(r, h.cfg, h.cache, rec, "write"); code != 0 {
		ErrorJSON(w, code, msg)
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

// PresignDownloadFile handles POST /api/buckets/{name}/files/{id}/presign
// Returns a time-limited shortlink URL for the given file.
func (h *BucketAdminHandler) PresignDownloadFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	id := chi.URLParam(r, "id")

	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}
	if code, msg := auth.AuthorizeBucket(r, h.cfg, h.cache, rec, "read"); code != 0 {
		ErrorJSON(w, code, msg)
		return
	}

	var body struct {
		ExpiresIn   int    `json:"expires_in"`
		Disposition string `json:"disposition"`
	}
	_ = decodeJSONOpt(r, &body)

	result, err := h.storage.PresignDownload(id, slug, files.PresignDownloadOpts{
		ExpiresIn: body.ExpiresIn,
	})
	if err != nil {
		if err == files.ErrFileNotFound {
			ErrorJSON(w, http.StatusNotFound, "File not found")
			return
		}
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	disp := body.Disposition
	if disp != "attachment" && disp != "inline" {
		disp = "inline"
	}
	url := scheme + "://" + r.Host + "/api/buckets/" + slug + "/files/" + id + "/download?token=" + result.Token + "&dis=" + disp

	writeJSON(w, http.StatusOK, map[string]any{
		"url":        url,
		"token_id":   result.TokenID,
		"expires_at": result.ExpiresAt.UTC().Format(time.RFC3339),
		"expires_in": result.ExpiresIn,
	})
}

// PresignUploadFile handles POST /api/buckets/{name}/files/presign-upload
// Returns a pre-authorised URL the caller can PUT file bytes to directly.
func (h *BucketAdminHandler) PresignUploadFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")

	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}
	if code, msg := auth.AuthorizeBucket(r, h.cfg, h.cache, rec, "write"); code != 0 {
		ErrorJSON(w, code, msg)
		return
	}

	var body struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		FolderPath  string `json:"folder_path"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Filename == "" {
		ErrorJSON(w, http.StatusBadRequest, "filename is required")
		return
	}

	result, err := h.storage.PresignUpload(files.PresignUploadOpts{
		Namespace:   slug,
		Filename:    body.Filename,
		ContentType: body.ContentType,
		FolderPath:  body.FolderPath,
		ExpiresIn:   body.ExpiresIn,
	})
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	uploadURL := scheme + "://" + r.Host + "/api/buckets/" + slug + "/files/upload?token=" + result.Token

	writeJSON(w, http.StatusOK, map[string]any{
		"upload_url":    uploadURL,
		"method":        "PUT",
		"confirm_url":   "",
		"confirm_token": "",
		"expires_at":    result.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// RevokeFileToken handles POST /api/buckets/{name}/tokens/files/revoke
// Revokes a files:read token so it can no longer be used via the shortlink.
func (h *BucketAdminHandler) RevokeFileToken(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")

	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}
	if code, msg := auth.AuthorizeBucket(r, h.cfg, h.cache, rec, "write"); code != 0 {
		ErrorJSON(w, code, msg)
		return
	}

	var body struct {
		TokenID   string `json:"token_id"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		Reason    string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	var tokenID, expiresAt string
	if body.Token != "" {
		payload, verifyErr := filetoken.Verify(body.Token)
		if verifyErr != nil || payload == nil {
			ErrorJSON(w, http.StatusBadRequest, "Invalid or expired token")
			return
		}
		if payload.DBName != slug {
			ErrorJSON(w, http.StatusBadRequest, "Token does not belong to this bucket")
			return
		}
		tokenID = payload.TokenID
		expiresAt = time.Unix(payload.ExpiresAt, 0).UTC().Format(time.RFC3339)
	} else {
		if body.TokenID == "" || body.ExpiresAt == "" {
			ErrorJSON(w, http.StatusBadRequest, "token or (token_id + expires_at) is required")
			return
		}
		tokenID = body.TokenID
		expiresAt = body.ExpiresAt
	}

	var reason *string
	if body.Reason != "" {
		reason = &body.Reason
	}
	if err := h.registry.RevokeFileToken(tokenID, slug, expiresAt, reason); err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// FileShortlink handles GET /api/buckets/{name}/files/{id}/download
// Serves a bucket file using a presigned HMAC token (files:read scope).
// No admin/API-key auth is required — only a valid file token.
func (h *BucketAdminHandler) FileShortlink(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	id := chi.URLParam(r, "id")
	ns := "bkt-" + slug

	if !filetoken.ValidateFromRequest(r, slug, func(tokenID string) bool {
		revoked, _ := h.registry.IsFileTokenRevoked(tokenID, slug)
		return revoked
	}) {
		ErrorJSON(w, http.StatusUnauthorized, "Missing or invalid file access token")
		return
	}

	stored, err := h.storage.GetByID(id)
	if err != nil || stored == nil || stored.DBName != ns {
		ErrorJSON(w, http.StatusNotFound, "File not found")
		return
	}

	if stored.ExpiresAt.Valid && stored.ExpiresAt.String != "" {
		expAt, parseErr := time.Parse("2006-01-02 15:04:05", stored.ExpiresAt.String)
		if parseErr == nil && time.Now().After(expAt) {
			ErrorJSON(w, http.StatusGone, "File has expired")
			return
		}
	}

	ct := stored.ContentType.String
	if ct == "" {
		ct = "application/octet-stream"
	}
	disp := r.URL.Query().Get("dis")
	if disp != "attachment" && disp != "inline" {
		disp = "inline"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(stored.SizeBytes, 10))
	w.Header().Set("Content-Disposition",
		disp+"; filename=\""+sanitizeHeaderFilename(stored.Filename)+`"`)
	w.Header().Set("X-Sendfile", "/"+filepath.Base(stored.StoragePath))
	w.WriteHeader(http.StatusOK)
}

// UploadShortlink handles PUT /api/buckets/{name}/files/upload?token=<files:write token>.
// Validates the upload token and stores the body as a new file in the bucket.
func (h *BucketAdminHandler) UploadShortlink(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "name")
	ns := "bkt-" + slug

	tokenRaw := r.URL.Query().Get("token")
	if tokenRaw == "" {
		tokenRaw = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if tokenRaw == "" {
		ErrorJSON(w, http.StatusUnauthorized, "Missing upload token")
		return
	}

	payload, err := filetoken.Verify(tokenRaw)
	if err != nil || payload == nil {
		ErrorJSON(w, http.StatusUnauthorized, "Invalid or expired upload token")
		return
	}
	if payload.Scope != "files:write" {
		ErrorJSON(w, http.StatusForbidden, "Token does not have upload permission")
		return
	}
	if payload.DBName != slug {
		ErrorJSON(w, http.StatusForbidden, "Token namespace mismatch")
		return
	}

	// Confirm bucket still exists.
	rec, err := h.registry.GetBucket(slug)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Bucket not found")
		return
	}

	filename := payload.Filename
	if filename == "" {
		filename = r.URL.Query().Get("filename")
	}
	if filename == "" {
		ErrorJSON(w, http.StatusBadRequest, "filename is required (query param or token)")
		return
	}
	contentType := payload.ContentType
	if contentType == "" {
		contentType = r.URL.Query().Get("content_type")
	}
	folderPath := payload.FolderPath
	if folderPath == "" {
		folderPath = r.URL.Query().Get("folder_path")
	}
	if payload.Filename != "" && r.URL.Query().Get("filename") != "" &&
		r.URL.Query().Get("filename") != payload.Filename {
		ErrorJSON(w, http.StatusBadRequest, "filename does not match upload token")
		return
	}

	data, readErr := io.ReadAll(io.LimitReader(r.Body, h.cfg.FileMaxSizeBytes+1))
	if readErr != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to read request body")
		return
	}
	if int64(len(data)) > h.cfg.FileMaxSizeBytes {
		ErrorJSON(w, http.StatusRequestEntityTooLarge, files.ErrFileTooLarge.Error())
		return
	}

	result, uploadErr := h.storage.Upload(files.UploadInput{
		DBName:      ns,
		Filename:    filename,
		Data:        data,
		ContentType: contentType,
		FolderPath:  folderPath,
	})
	if uploadErr != nil {
		ErrorJSON(w, http.StatusInternalServerError, uploadErr.Error())
		return
	}

	storedFile, _ := h.storage.GetByID(result.ID)
	if storedFile == nil {
		writeJSON(w, http.StatusCreated, map[string]any{"id": result.ID, "size_bytes": result.SizeBytes})
		return
	}
	writeJSON(w, http.StatusCreated, bucketFileRecord(storedFile, slug))
}

// sanitizeHeaderFilename removes characters that are unsafe to embed in an HTTP
// header value, preventing response-splitting and header-injection attacks.
func sanitizeHeaderFilename(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch r {
		case '\r', '\n', '\x00', '"', ';', '\\':
			// Drop unsafe characters.
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
