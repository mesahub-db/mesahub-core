// Package handler — system.go exposes access to the internal store.db
// database via admin-only HTTP endpoints.
//
// Routes (all require RequireAdmin middleware):
//
//	GET  /api/system/dbs               — list system databases with sizes
//	POST /api/system/db/{name}/query   — run a read-only SELECT against a system db
//	POST /api/system/db/{name}/exec    — run a write statement against a system db
//	                                     (only when ENABLE_SYSTEM_DB_WRITE=true)
package handler

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	_ "github.com/mattn/go-sqlite3"

	"github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/queue"
	"github.com/0xdps/mesahub-core/sysutil"
)

// systemDBNames is the ordered list of recognised system database names.
var systemDBNames = []string{"store"}

// systemDBMeta holds display metadata for each system database.
var systemDBMeta = map[string]struct {
	label       string
	description string
}{
	"store": {
		label:       "store.db",
		description: "Source of truth for all user databases on this instance",
	},
}

// onlySelectPattern rejects any SQL that isn't a read-only statement.
var onlySelectPattern = regexp.MustCompile(
	`(?i)^\s*(SELECT|WITH|VALUES|EXPLAIN|PRAGMA\s+\w+\s*([^=]|$))`)

// SystemHandler exposes system / internal databases to admin users.
type SystemHandler struct {
	cfg   *config.Config
	queue *queue.Queue
}

// NewSystemHandler creates a SystemHandler.
func NewSystemHandler(cfg *config.Config, q *queue.Queue) *SystemHandler {
	return &SystemHandler{cfg: cfg, queue: q}
}

// systemDBPath returns the filesystem path for a named system database.
func (h *SystemHandler) systemDBPath(name string) string {
	return filepath.Join(h.cfg.DataPath, name+".db")
}

// systemDBExists reports whether the file for a system db is present on disk.
func (h *SystemHandler) systemDBExists(name string) bool {
	_, err := os.Stat(h.systemDBPath(name))
	return err == nil
}

// ListSystemDBs handles GET /api/system/dbs.
// Returns the list of system databases that exist on disk, with their sizes.
func (h *SystemHandler) ListSystemDBs(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Name        string `json:"name"`
		Label       string `json:"label"`
		Description string `json:"description"`
		SizeBytes   int64  `json:"size_bytes"`
	}

	out := make([]entry, 0, len(systemDBNames))
	for _, name := range systemDBNames {
		path := h.systemDBPath(name)
		if _, err := os.Stat(path); err != nil {
			// File doesn't exist yet (e.g. fresh standalone data dir).
			continue
		}
		size := sysutil.FileSizeBytes(path)
		meta := systemDBMeta[name]
		out = append(out, entry{
			Name:        name,
			Label:       meta.label,
			Description: meta.description,
			SizeBytes:   size,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// QuerySystemDB handles POST /api/system/db/{name}/query.
// Runs a read-only SELECT against the named system database.
// Only SELECT / WITH / VALUES / EXPLAIN / read-only PRAGMA are permitted.
func (h *SystemHandler) QuerySystemDB(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Validate name against the known allowlist — prevents path traversal.
	allowed := false
	for _, n := range systemDBNames {
		if n == name {
			allowed = true
			break
		}
	}
	if !allowed {
		ErrorJSON(w, http.StatusNotFound, "unknown system database")
		return
	}

	if !h.systemDBExists(name) {
		ErrorJSON(w, http.StatusNotFound, "system database not available")
		return
	}

	var body struct {
		SQL      string `json:"sql"`
		Bindings []any  `json:"bindings"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.SQL == "" {
		ErrorJSON(w, http.StatusBadRequest, "sql is required")
		return
	}
	if !onlySelectPattern.MatchString(body.SQL) {
		ErrorJSON(w, http.StatusForbidden,
			"only read-only statements are permitted on system databases")
		return
	}

	dbPath := h.systemDBPath(name)
	sqlDB, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to open system database: "+err.Error())
		return
	}
	defer sqlDB.Close()

	start := time.Now()
	rows, err := sqlDB.QueryContext(r.Context(), body.SQL, anySliceToDriverValues(body.Bindings)...)
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	defer rows.Close()

	headers, rowData, rowsRead, err := scanRows(rows)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	elapsed := time.Since(start).Milliseconds()

	writeJSON(w, http.StatusOK, map[string]any{
		"headers": headers,
		"rows":    rowData,
		"stat": map[string]any{
			"rowsAffected":    0,
			"rowsRead":        rowsRead,
			"rowsWritten":     nil,
			"queryDurationMs": elapsed,
		},
	})
}

// dangerousDDLPattern matches statements that could corrupt a system database
// schema (DROP TABLE, ALTER TABLE, DROP INDEX, DROP TRIGGER, DROP VIEW).
// These are blocked even when write access is enabled.
var dangerousDDLPattern = regexp.MustCompile(
	`(?i)^\s*(DROP\s+(TABLE|INDEX|TRIGGER|VIEW)|ALTER\s+TABLE)`)

// ExecSystemDB handles POST /api/system/db/{name}/exec.
// Executes a write statement against the named system database.
// Only available when ENABLE_SYSTEM_DB_WRITE=true; DDL that drops or alters
// schema objects is always blocked.
func (h *SystemHandler) ExecSystemDB(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.EnableSystemDBWrite {
		ErrorJSON(w, http.StatusForbidden,
			"system database write access is disabled; set ENABLE_SYSTEM_DB_WRITE=true to enable")
		return
	}

	name := chi.URLParam(r, "name")

	allowed := false
	for _, n := range systemDBNames {
		if n == name {
			allowed = true
			break
		}
	}
	if !allowed {
		ErrorJSON(w, http.StatusNotFound, "unknown system database")
		return
	}

	if !h.systemDBExists(name) {
		ErrorJSON(w, http.StatusNotFound, "system database not available")
		return
	}

	var body struct {
		SQL      string `json:"sql"`
		Bindings []any  `json:"bindings"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.SQL == "" {
		ErrorJSON(w, http.StatusBadRequest, "sql is required")
		return
	}
	if dangerousDDLPattern.MatchString(body.SQL) {
		ErrorJSON(w, http.StatusForbidden,
			"DROP and ALTER TABLE statements are not permitted on system databases")
		return
	}

	dbPath := h.systemDBPath(name)

	var rowsAffected int64
	var elapsed int64

	err := h.queue.Enqueue(r.Context(), "system:"+name, func() error {
		sqlDB, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
		if err != nil {
			return err
		}
		defer sqlDB.Close()

		start := time.Now()
		res, err := sqlDB.ExecContext(r.Context(), body.SQL, anySliceToDriverValues(body.Bindings)...)
		elapsed = time.Since(start).Milliseconds()
		if err != nil {
			return err
		}
		rowsAffected, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stat": map[string]any{
			"rowsAffected":    rowsAffected,
			"rowsRead":        nil,
			"rowsWritten":     rowsAffected,
			"queryDurationMs": elapsed,
		},
	})
}
