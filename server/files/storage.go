// Package files — storage.go implements the high-level file upload / download /
// delete operations that sit on top of MetadataDB. Mirrors the upload half of
// file-storage.ts, including: MIME detection, limit enforcement, content-hash
// deduplication, blob GC, and the replace / error conflict modes.
package files

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/0xdps/mesahub-core/config"
)

// ── limit defaults ────────────────────────────────────────────────────────────

const (
	defaultMaxFileBytes    int64 = 100 * 1024 * 1024 // 100 MB
	defaultMaxFilesPerDB   int64 = 10_000
	defaultMaxStorageBytes int64 = 5 * 1024 * 1024 * 1024 // 5 GB
	defaultMaxFilenameLen  int   = 255
	defaultMaxMetadataLen  int   = 4096
	maxFolderPathLen             = 512

	defaultAllowedMimes = "image/*,video/*,audio/*,application/pdf,application/json,application/zip,application/gzip,text/*"
)

// Limits holds the active storage constraints.
type Limits struct {
	MaxFileSizeBytes    int64
	MaxFilesPerDB       int64
	MaxStoragePerDB     int64
	MaxFilenameLength   int
	MaxMetadataBytes    int
	AllowedMimePatterns []string
}

// LimitsFromConfig builds Limits from the application config, falling back to
// the same environment variables file-storage.ts reads.
func LimitsFromConfig(_ *config.Config) Limits {
	l := Limits{
		MaxFileSizeBytes:  parseBytesEnv("FILE_MAX_SIZE_BYTES", defaultMaxFileBytes),
		MaxFilesPerDB:     parseInt64Env("FILE_MAX_FILES_PER_DB", defaultMaxFilesPerDB),
		MaxStoragePerDB:   parseBytesEnv("FILE_MAX_STORAGE_PER_DB_BYTES", defaultMaxStorageBytes),
		MaxFilenameLength: parseIntEnv("FILE_MAX_FILENAME_LENGTH", defaultMaxFilenameLen),
		MaxMetadataBytes:  parseIntEnv("FILE_MAX_METADATA_BYTES", defaultMaxMetadataLen),
	}
	patterns := os.Getenv("FILE_ALLOWED_MIME_PATTERNS")
	if patterns == "" {
		patterns = defaultAllowedMimes
	}
	for _, p := range strings.Split(patterns, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			l.AllowedMimePatterns = append(l.AllowedMimePatterns, p)
		}
	}
	return l
}

// ── error types ───────────────────────────────────────────────────────────────

var (
	ErrFileTooLarge      = errors.New("file exceeds maximum size limit")
	ErrTooManyFiles      = errors.New("database has reached the maximum file count")
	ErrStorageQuota      = errors.New("database has reached the storage quota")
	ErrMimeNotAllowed    = errors.New("file MIME type is not permitted")
	ErrMimeMismatch      = errors.New("detected MIME type does not match claimed type")
	ErrConflict          = errors.New("a file with that name already exists")
	ErrInvalidConflict   = errors.New("conflictMode must be 'replace' or 'error'")
	ErrFilenameTooLong   = errors.New("filename exceeds maximum length")
	ErrMetadataTooLarge  = errors.New("metadata field exceeds maximum size")
	ErrFolderPathInvalid = errors.New("folderPath contains invalid characters")
	ErrFileNotFound      = errors.New("file not found")
)

// ── upload / delete types ─────────────────────────────────────────────────────

// ConflictMode controls what happens when a file with the same logical path exists.
type ConflictMode string

const (
	ConflictReplace ConflictMode = "replace"
	ConflictError   ConflictMode = "error"
)

// UploadInput is the caller-supplied file data and metadata.
type UploadInput struct {
	DBName       string
	FolderPath   string
	Filename     string
	ContentType  string
	Data         []byte
	ConflictMode ConflictMode
	ExpiresAt    sql.NullString
	Metadata     sql.NullString
}

// UploadResult is returned after a successful upload.
type UploadResult struct {
	ID           string
	ContentHash  string
	SizeBytes    int64
	Deduplicated bool // true if the blob already existed on disk
}

// ── Storage ───────────────────────────────────────────────────────────────────

// Storage orchestrates high-level file operations using MetadataDB under the hood.
type Storage struct {
	meta     *MetadataDB
	blobsDir string
	limits   Limits
}

// NewStorage opens (or creates) the files area at dataPath/files/.
func NewStorage(dataPath string, cfg *config.Config) (*Storage, error) {
	filesRoot := filepath.Join(dataPath, "files")
	meta, err := OpenMetadataDB(filesRoot)
	if err != nil {
		return nil, err
	}
	return &Storage{
		meta:     meta,
		blobsDir: filepath.Join(filesRoot, "blobs"),
		limits:   LimitsFromConfig(cfg),
	}, nil
}

// Close shuts down the metadata connection.
func (s *Storage) Close() error { return s.meta.Close() }

// Meta returns the underlying MetadataDB. Used by cloud storage backends
// (S3/R2) that manage blobs directly but share the SQLite metadata layer.
func (s *Storage) Meta() *MetadataDB { return s.meta }

// BlobsDir returns the path to the local blobs directory.
func (s *Storage) BlobsDir() string { return s.blobsDir }

// Limits returns the configured storage limits.
func (s *Storage) StorageLimits() Limits { return s.limits }

// SumBytesForNamespace returns the total size_bytes of all files stored under
// the given namespace (db_name), for use in bucket size tracking.
func (s *Storage) SumBytesForNamespace(ns string) int64 {
	_, total, err := s.meta.DBUsageStats(ns)
	if err != nil {
		return 0
	}
	return total
}

// ── Upload ────────────────────────────────────────────────────────────────────

// Upload validates, stores, and records an uploaded file. It is idempotent when
// the same content is uploaded under the same logical path (no extra blob written).
func (s *Storage) Upload(in UploadInput) (UploadResult, error) {
	// 1. Normalise filename / folder path
	in.Filename = normaliseFilename(in.Filename, s.limits.MaxFilenameLength)
	var err error
	in.FolderPath, err = normaliseFolder(in.FolderPath)
	if err != nil {
		return UploadResult{}, ErrFolderPathInvalid
	}

	// 2. Validate conflict mode
	if in.ConflictMode == "" {
		in.ConflictMode = ConflictReplace
	}
	if in.ConflictMode != ConflictReplace && in.ConflictMode != ConflictError {
		return UploadResult{}, ErrInvalidConflict
	}

	// 3. Size check
	dataLen := int64(len(in.Data))
	if dataLen > s.limits.MaxFileSizeBytes {
		return UploadResult{}, ErrFileTooLarge
	}

	// 4. Metadata size check
	if in.Metadata.Valid && len(in.Metadata.String) > s.limits.MaxMetadataBytes {
		return UploadResult{}, ErrMetadataTooLarge
	}

	// 5. MIME validation
	detected := detectMIME(in.Data)
	if detected != "" && in.ContentType != "" && detected != in.ContentType {
		return UploadResult{}, ErrMimeMismatch
	}
	effectiveMime := in.ContentType
	if effectiveMime == "" {
		effectiveMime = detected
	}
	if !mimeAllowed(effectiveMime, s.limits.AllowedMimePatterns) {
		return UploadResult{}, ErrMimeNotAllowed
	}

	// 6. Per-db limits
	fileCount, totalBytes, err := s.meta.DBUsageStats(in.DBName)
	if err != nil {
		return UploadResult{}, fmt.Errorf("files: usage stats: %w", err)
	}

	// 7. Conflict check
	existing, err := s.meta.GetByLogicalPath(in.DBName, in.FolderPath, in.Filename)
	if err != nil {
		return UploadResult{}, fmt.Errorf("files: lookup: %w", err)
	}
	if existing != nil {
		if in.ConflictMode == ConflictError {
			return UploadResult{}, ErrConflict
		}
		// Will replace; don't count the existing file against the limits below.
		fileCount--
		totalBytes -= existing.SizeBytes
	}

	if fileCount >= s.limits.MaxFilesPerDB {
		return UploadResult{}, ErrTooManyFiles
	}
	if totalBytes+dataLen > s.limits.MaxStoragePerDB {
		return UploadResult{}, ErrStorageQuota
	}

	// 8. Compute content hash
	sum := sha256.Sum256(in.Data)
	hash := hex.EncodeToString(sum[:])

	// 9. Write blob if new
	blobPath := filepath.Join(s.blobsDir, hash)
	deduplicated := false
	if _, statErr := os.Stat(blobPath); os.IsNotExist(statErr) {
		if err := writeBlob(blobPath, in.Data); err != nil {
			return UploadResult{}, fmt.Errorf("files: write blob: %w", err)
		}
	} else {
		deduplicated = true
	}

	// 10. Persist metadata
	now := NowUTC()
	newID := uuid.NewString()
	rec := &StoredFile{
		ID:          newID,
		DBName:      in.DBName,
		ContentHash: hash,
		Filename:    in.Filename,
		FolderPath:  in.FolderPath,
		ContentType: sql.NullString{String: effectiveMime, Valid: effectiveMime != ""},
		SizeBytes:   dataLen,
		StoragePath: blobPath,
		UploadedAt:  now,
		ExpiresAt:   in.ExpiresAt,
		Metadata:    in.Metadata,
	}

	if existing != nil {
		// Replace: swap metadata rows and adjust blob refs atomically.
		oldHash, err := s.meta.ReplaceFile(existing.ID, rec)
		if err != nil {
			return UploadResult{}, fmt.Errorf("files: replace metadata: %w", err)
		}
		// Increment ref for the new hash, then decrement for the old.
		if err := s.meta.IncrBlobRef(hash); err != nil {
			return UploadResult{}, fmt.Errorf("files: incr blob ref: %w", err)
		}
		remaining, err := s.meta.DecrBlobRef(oldHash)
		if err != nil {
			return UploadResult{}, fmt.Errorf("files: decr blob ref: %w", err)
		}
		if remaining <= 0 && oldHash != hash {
			_ = os.Remove(filepath.Join(s.blobsDir, oldHash))
		}
	} else {
		if err := s.meta.InsertFile(rec); err != nil {
			return UploadResult{}, fmt.Errorf("files: insert metadata: %w", err)
		}
		if err := s.meta.IncrBlobRef(hash); err != nil {
			return UploadResult{}, fmt.Errorf("files: incr blob ref: %w", err)
		}
	}

	return UploadResult{
		ID:           newID,
		ContentHash:  hash,
		SizeBytes:    dataLen,
		Deduplicated: deduplicated,
	}, nil
}

// ── Read ──────────────────────────────────────────────────────────────────────

// GetBlobReader returns a reader for the blob content of a file.
func (s *Storage) GetBlobReader(contentHash string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(s.blobsDir, contentHash))
}

// GetBlobBytes reads the entire blob into memory. Prefer GetBlobReader for large files.
func (s *Storage) GetBlobBytes(contentHash string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.blobsDir, contentHash))
}

// GetByID returns the file record for an ID, or nil if not found.
func (s *Storage) GetByID(id string) (*StoredFile, error) {
	return s.meta.GetByID(id)
}

// List returns a paginated list of files for a database.
// sort: uploaded_at | filename | size_bytes | content_type (default: uploaded_at)
// order: asc | desc (default: desc)
func (s *Storage) List(dbName string, limit, offset int, folderPrefix, sort, order string) (ListFilesResult, error) {
	return s.meta.List(dbName, limit, offset, folderPrefix, sort, order)
}

// StorageMetrics returns aggregate usage stats.
func (s *Storage) StorageMetrics() (MetricsResult, error) {
	return s.meta.StorageMetrics()
}

// ── Delete ────────────────────────────────────────────────────────────────────

// DeleteByID removes a single file. The blob is deleted only when its ref count drops to 0.
func (s *Storage) DeleteByID(id, dbName string) error {
	rec, err := s.meta.GetByID(id)
	if err != nil {
		return err
	}
	if rec == nil || rec.DBName != dbName {
		return fmt.Errorf("files: not found or wrong db")
	}

	hash, err := s.meta.DeleteFile(id)
	if err != nil {
		return fmt.Errorf("files: delete metadata: %w", err)
	}
	remaining, err := s.meta.DecrBlobRef(hash)
	if err != nil {
		return fmt.Errorf("files: decr blob ref: %w", err)
	}
	if remaining <= 0 {
		_ = os.Remove(filepath.Join(s.blobsDir, hash))
	}
	return nil
}

// DeleteAllForDatabase removes every file belonging to a database.
func (s *Storage) DeleteAllForDatabase(dbName string) error {
	hashes, err := s.meta.DeleteFilesForDatabase(dbName)
	if err != nil {
		return fmt.Errorf("files: delete db files: %w", err)
	}
	for _, h := range hashes {
		remaining, err := s.meta.DecrBlobRef(h)
		if err != nil {
			continue
		}
		if remaining <= 0 {
			_ = os.Remove(filepath.Join(s.blobsDir, h))
		}
	}
	return nil
}

// CleanupExpired removes all files whose expires_at has passed and GCs orphan blobs.
func (s *Storage) CleanupExpired() error {
	hashes, err := s.meta.CleanupExpiredFiles()
	if err != nil {
		return err
	}
	for _, h := range hashes {
		remaining, err := s.meta.DecrBlobRef(h)
		if err != nil {
			continue
		}
		if remaining <= 0 {
			_ = os.Remove(filepath.Join(s.blobsDir, h))
		}
	}
	return nil
}

// ── MIME helpers ──────────────────────────────────────────────────────────────

// detectMIME identifies the content type from magic bytes, mirroring sniffMimeType
// in file-storage.ts.
func detectMIME(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47}):
		return "image/png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "application/pdf"
	case bytes.HasPrefix(data, []byte{0x50, 0x4B, 0x03, 0x04}):
		return "application/zip"
	case bytes.HasPrefix(data, []byte{0x1F, 0x8B}):
		return "application/gzip"
	default:
		return ""
	}
}

// mimeAllowed checks whether a MIME type satisfies any of the allowed patterns.
// Patterns may use a wildcard subtype, e.g. "image/*".
func mimeAllowed(mime string, patterns []string) bool {
	if mime == "" {
		return true // unknown MIME — defer to caller's trust
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == mime {
			return true
		}
		if strings.HasSuffix(p, "/*") {
			prefix := strings.TrimSuffix(p, "/*")
			if strings.HasPrefix(mime, prefix+"/") {
				return true
			}
		}
		// regex-style pattern support (e.g. "application/.*")
		if matched, _ := regexp.MatchString("^"+p+"$", mime); matched {
			return true
		}
	}
	return false
}

// ── filename / folder normalisation ──────────────────────────────────────────

func normaliseFilename(name string, maxLen int) string {
	name = filepath.Base(strings.TrimSpace(name))
	if maxLen > 0 && len(name) > maxLen {
		name = name[:maxLen]
	}
	return name
}

func normaliseFolder(path string) (string, error) {
	path = strings.TrimSpace(path)
	// Reject directory traversal
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return "", ErrFolderPathInvalid
		}
	}
	path = strings.Trim(path, "/")
	if len(path) > maxFolderPathLen {
		path = path[:maxFolderPathLen]
	}
	return path, nil
}

// ── blob I/O ──────────────────────────────────────────────────────────────────

func writeBlob(dst string, data []byte) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// ── env helpers ───────────────────────────────────────────────────────────────

func parseBytesEnv(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func parseInt64Env(key string, def int64) int64 {
	return parseBytesEnv(key, def)
}

func parseIntEnv(key string, def int) int {
	v := parseInt64Env(key, int64(def))
	if v > math.MaxInt32 {
		return def
	}
	return int(v)
}
