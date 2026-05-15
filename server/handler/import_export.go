// Package handler — import_export.go handles SQLite file import and export.
//
// POST /api/db/{name}/import
//   - Multipart form: file = SQLite bytes, tables = JSON array (optional)
//   - Phase 1 (no tables): inspects the uploaded file, returns table list
//   - Phase 2 (tables supplied): uses ATTACH DATABASE for efficient bulk copy
//
// POST /api/db/{name}/export
//   - JSON body: { tables: string[], filename?: string }
//   - Uses ATTACH DATABASE to copy selected tables to a temp SQLite file
//   - Streams the result back as application/octet-stream
package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/0xdps/mesahub-core/auth"
	"github.com/0xdps/mesahub-core/cache"
	"github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/db"
	"github.com/0xdps/mesahub-core/queue"
	"github.com/0xdps/mesahub-core/sysutil"

	_ "github.com/mattn/go-sqlite3" // CGO SQLite driver
)

// importMaxBytes caps the size of an uploaded SQLite file at 100 MB.
// This matches the default FileMaxSizeBytes config value and is consistent
// with the Go HTTP ReadTimeout (raised to 300 s for upload paths), which
// allows a 100 MB file to upload at ~2.7 Mbit/s before timing out.
const importMaxBytes = 100 << 20

var (
	// createTableSchemaPrefixRe matches the CREATE TABLE [IF NOT EXISTS] prefix
	// so we can inject a schema name before the table name.
	createTableSchemaPrefixRe = regexp.MustCompile(`(?i)^(CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?)`)

	// createIfNotExistsRe rewrites CREATE TABLE ... to CREATE TABLE IF NOT EXISTS ...
	createIfNotExistsRe = regexp.MustCompile(`(?i)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?`)
)

// ImportExportHandler holds dependencies for import/export routes.
type ImportExportHandler struct {
	cfg      *config.Config
	pool     *db.Pool
	queue    *queue.Queue
	registry *db.Registry
	cache    cache.Client
}

// NewImportExportHandler constructs an ImportExportHandler.
func NewImportExportHandler(cfg *config.Config, pool *db.Pool, wq *queue.Queue, registry *db.Registry, c cache.Client) *ImportExportHandler {
	return &ImportExportHandler{cfg: cfg, pool: pool, queue: wq, registry: registry, cache: c}
}

// ── Import ────────────────────────────────────────────────────────────────────

// Import handles POST /api/db/{name}/import.
// Phase 1 (no `tables` form field): streams the uploaded SQLite file to disk,
// inspects its schema, and returns the table list.
// Phase 2 (`tables` form field present): re-reads the file and bulk-copies the
// selected tables into the target database via ATTACH DATABASE.
func (h *ImportExportHandler) Import(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	rec, err := h.registry.GetDatabase(name)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Database not found")
		return
	}

	if code, msg := auth.AuthorizeDB(r, h.cfg, h.cache, rec); code != 0 {
		ErrorJSON(w, code, msg)
		return
	}

	// Parse the multipart form.
	// Memory threshold for part buffering; anything larger spills to disk.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		ErrorJSON(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	// Stream the uploaded file to a temp path — no full in-memory load.
	tmpFile, err := os.CreateTemp("", "mesahub-import-*.db")
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to create temp file")
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	written, err := io.Copy(tmpFile, io.LimitReader(file, importMaxBytes+1))
	tmpFile.Close()
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to read uploaded file")
		return
	}
	if written > importMaxBytes {
		ErrorJSON(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file exceeds the maximum import size of %d MB", importMaxBytes>>20))
		return
	}

	// Validate SQLite magic bytes.
	if !isSQLiteFile(tmpPath) {
		ErrorJSON(w, http.StatusBadRequest, "not a valid SQLite database file")
		return
	}

	tablesParam := r.FormValue("tables")

	if tablesParam == "" {
		// ── Phase 1: Inspect ──────────────────────────────────────────────────
		tables, err := inspectSQLite(tmpPath)
		if err != nil {
			ErrorJSON(w, http.StatusInternalServerError, "failed to inspect database: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tables": tables})
		return
	}

	// ── Phase 2: Import ───────────────────────────────────────────────────────
	var selectedTables []string
	if err := json.Unmarshal([]byte(tablesParam), &selectedTables); err != nil || len(selectedTables) == 0 {
		ErrorJSON(w, http.StatusBadRequest, "tables must be a non-empty JSON array of table names")
		return
	}

	var totalImported int64
	queueErr := h.queue.Enqueue(r.Context(), name, func() error {
		targetDB, err := h.pool.Get(name)
		if err != nil {
			return fmt.Errorf("open target db: %w", err)
		}

		if _, err := targetDB.Exec("ATTACH DATABASE ? AS import_src", tmpPath); err != nil {
			return fmt.Errorf("attach source db: %w", err)
		}
		defer func() {
			if _, err := targetDB.Exec("DETACH DATABASE import_src"); err != nil {
				log.Warn().Err(err).Str("db", name).Msg("failed to detach import_src")
			}
		}()

		for _, tableName := range selectedTables {
			safe := `"` + strings.ReplaceAll(tableName, `"`, `""`) + `"`

			// Fetch original DDL from the source.
			var createSQL string
			if err := targetDB.QueryRow(
				"SELECT sql FROM import_src.sqlite_master WHERE type='table' AND name=?", tableName,
			).Scan(&createSQL); err != nil {
				log.Warn().Str("table", tableName).Msg("table not found in import source, skipping")
				continue
			}

			// Ensure the table exists in the target.
			safeCreate := createIfNotExistsRe.ReplaceAllString(createSQL, "CREATE TABLE IF NOT EXISTS ")
			if _, err := targetDB.Exec(safeCreate); err != nil {
				return fmt.Errorf("create table %s: %w", tableName, err)
			}

			// Bulk copy via ATTACH — SQLite B-tree level transfer, no row iteration.
			res, err := targetDB.Exec(fmt.Sprintf(
				"INSERT OR IGNORE INTO main.%s SELECT * FROM import_src.%s", safe, safe,
			))
			if err != nil {
				return fmt.Errorf("copy table %s: %w", tableName, err)
			}
			n, _ := res.RowsAffected()
			totalImported += n
		}
		return nil
	})
	if queueErr != nil {
		ErrorJSON(w, http.StatusInternalServerError, "import failed: "+queueErr.Error())
		return
	}

	// Return the post-import file size so the dashboard can persist it immediately.
	sizeBytes := sysutil.FileSizeBytes(sysutil.DBPath(h.cfg.DataPath, name))

	// Persist the new size to the registry (fire-and-forget).
	go func() {
		if err := h.registry.UpdateDatabaseSize(name, sizeBytes); err != nil {
			log.Warn().Err(err).Str("db", name).Msg("[import] failed to update size_bytes")
		}
	}()

	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"totalImported": totalImported,
		"tables":        selectedTables,
		"size_bytes":    sizeBytes,
	})
}

// ── Export ────────────────────────────────────────────────────────────────────

// Export handles POST /api/db/{name}/export.
// Builds a new SQLite file containing the requested tables (schema + data) and
// streams it as an application/octet-stream download.
func (h *ImportExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	rec, err := h.registry.GetDatabase(name)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Database not found")
		return
	}

	if code, msg := auth.AuthorizeDB(r, h.cfg, h.cache, rec); code != 0 {
		ErrorJSON(w, code, msg)
		return
	}

	var body struct {
		Tables   []string `json:"tables"`
		Filename string   `json:"filename"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Tables) == 0 {
		ErrorJSON(w, http.StatusBadRequest, "tables array is required and must not be empty")
		return
	}

	filename := body.Filename
	if filename == "" {
		filename = name + ".sqlite"
	}

	// Create a temp file for the export database.
	tmpFile, err := os.CreateTemp("", "mesahub-export-*.db")
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to create temp file")
		return
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close() // Close so SQLite can open it.
	defer os.Remove(tmpPath)

	targetDB, err := h.pool.Get(name)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to open database")
		return
	}

	// Attach the temp file as export_dst.
	if _, err := targetDB.Exec("ATTACH DATABASE ? AS export_dst", tmpPath); err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "attach export db: "+err.Error())
		return
	}
	defer func() {
		if _, err := targetDB.Exec("DETACH DATABASE export_dst"); err != nil {
			log.Warn().Err(err).Str("db", name).Msg("failed to detach export_dst")
		}
	}()

	for _, tableName := range body.Tables {
		// Skip SQLite internal tables — they are reserved and cannot be created manually.
		if strings.HasPrefix(tableName, "sqlite_") {
			continue
		}
		safe := `"` + strings.ReplaceAll(tableName, `"`, `""`) + `"`

		// Fetch the original CREATE TABLE DDL.
		var createSQL string
		if err := targetDB.QueryRow(
			"SELECT sql FROM main.sqlite_master WHERE type='table' AND name=?", tableName,
		).Scan(&createSQL); err != nil {
			log.Warn().Str("table", tableName).Msg("table not found in export source, skipping")
			continue
		}

		// Rewrite DDL to target the export_dst schema.
		exportCreateSQL := createTableSchemaPrefixRe.ReplaceAllString(createSQL, "${1}export_dst.")
		if _, err := targetDB.Exec(exportCreateSQL); err != nil {
			ErrorJSON(w, http.StatusInternalServerError,
				fmt.Sprintf("create table %s in export db: %s", tableName, err.Error()))
			return
		}

		// Bulk copy via ATTACH — reads from main, writes to export_dst.
		if _, err := targetDB.Exec(fmt.Sprintf(
			"INSERT INTO export_dst.%s SELECT * FROM main.%s", safe, safe,
		)); err != nil {
			ErrorJSON(w, http.StatusInternalServerError,
				fmt.Sprintf("copy table %s: %s", tableName, err.Error()))
			return
		}
	}

	// Detach before reading the file, to ensure all data is flushed.
	if _, err := targetDB.Exec("DETACH DATABASE export_dst"); err != nil {
		log.Warn().Err(err).Str("db", name).Msg("failed to detach export_dst before send")
	}

	// Stream the temp file as a binary download.
	f, err := os.Open(tmpPath)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to open export file")
		return
	}
	defer f.Close()

	stat, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitizeDispositionFilename(filename)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	if _, err := io.Copy(w, f); err != nil {
		log.Error().Err(err).Str("db", name).Msg("failed to stream export file")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// inspectSQLite opens a SQLite file read-only and returns a summary of its tables.
func inspectSQLite(path string) ([]map[string]any, error) {
	sqlDB, err := sql.Open("sqlite3", path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer sqlDB.Close()

	rows, err := sqlDB.Query(
		"SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []map[string]any
	for rows.Next() {
		var tName, createSQL string
		if err := rows.Scan(&tName, &createSQL); err != nil {
			continue
		}
		safe := `"` + strings.ReplaceAll(tName, `"`, `""`) + `"`
		var rowCount int64
		sqlDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", safe)).Scan(&rowCount) //nolint:errcheck
		tables = append(tables, map[string]any{
			"name":      tName,
			"rowCount":  rowCount,
			"createSql": createSQL,
		})
	}
	if tables == nil {
		tables = []map[string]any{}
	}
	return tables, rows.Err()
}

// isSQLiteFile checks the first 16 bytes for the SQLite magic header.
func isSQLiteFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 16)
	n, _ := f.Read(magic)
	return n == 16 && strings.HasPrefix(string(magic), "SQLite format 3")
}

// sanitizeDispositionFilename strips characters that could break a Content-Disposition header.
func sanitizeDispositionFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, s)
}
