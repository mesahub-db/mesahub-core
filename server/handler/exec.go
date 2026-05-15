// Package handler — exec.go handles POST /api/db/:name/exec.
package handler

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/0xdps/mesahub-core/auth"
	"github.com/0xdps/mesahub-core/cache"
	"github.com/0xdps/mesahub-core/config"
	"github.com/0xdps/mesahub-core/db"
	"github.com/0xdps/mesahub-core/queue"
	"github.com/0xdps/mesahub-core/sysutil"
	"github.com/0xdps/mesahub-core/telemetry"
)

// rateLimitPerMinute is the maximum number of exec/query requests per API key
// per minute when Redis is available. Admin requests are not rate-limited.
const rateLimitPerMinute = 600

// SQL classifiers (case-insensitive prefix match).
var (
	readSQLPat  = regexp.MustCompile(`(?i)^\s*(SELECT|WITH|VALUES|EXPLAIN|PRAGMA\s+\w+\s*([^=]|$))`)
	writeSQLPat = regexp.MustCompile(`(?i)^\s*(INSERT|UPDATE|DELETE|DROP|ALTER|CREATE|ATTACH|DETACH|REPLACE|UPSERT|PRAGMA\s+\w+\s*=)`)
)

// classifySQL classifies a potentially multi-statement SQL batch.
// It splits on semicolons and returns "write" if ANY statement looks like a
// write, "read" if all recognisable statements are reads, and "unknown"
// otherwise. This prevents a batch like "SELECT 1; DROP TABLE users" from
// being routed down the read-only path.
func classifySQL(s string) string {
	parts := strings.Split(s, ";")
	hasRead := false
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		if writeSQLPat.MatchString(trimmed) {
			return "write"
		}
		if readSQLPat.MatchString(trimmed) {
			hasRead = true
			continue
		}
		// Unknown prefix — treat conservatively as write.
		return "write"
	}
	if hasRead {
		return "read"
	}
	return "unknown"
}

// ExecHandler holds dependencies for exec execution.
type ExecHandler struct {
	cfg      *config.Config
	pool     *db.Pool
	queue    *queue.Queue
	registry *db.Registry
	cache    cache.Client
	tel      *telemetry.Counters
}

// NewExecHandler creates an ExecHandler.
func NewExecHandler(cfg *config.Config, pool *db.Pool, wq *queue.Queue, registry *db.Registry, c cache.Client, tel *telemetry.Counters) *ExecHandler {
	return &ExecHandler{cfg: cfg, pool: pool, queue: wq, registry: registry, cache: c, tel: tel}
}

// Exec handles POST /api/db/:name/exec.
func (h *ExecHandler) Exec(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	rec, err := h.registry.GetDatabase(name)
	if err != nil || rec == nil {
		ErrorJSON(w, http.StatusNotFound, "Database not found")
		return
	}
	code, msg, kv := auth.AuthorizeDBWithKey(r, h.cfg, h.cache, rec)
	if code != 0 {
		ErrorJSON(w, code, msg)
		return
	}
	// Rate-limit API key requests (no-op when Redis is unavailable).
	if kv != nil {
		count, _ := h.cache.IncrRateLimit(r.Context(), kv.KeyID, time.Minute)
		if count > rateLimitPerMinute {
			ErrorJSON(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
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
	if h.cfg.MaxSQLLength > 0 && len(body.SQL) > h.cfg.MaxSQLLength {
		ErrorJSON(w, http.StatusBadRequest, "sql exceeds maximum allowed length")
		return
	}
	maxBindings := h.cfg.MaxSQLBindings
	if maxBindings <= 0 {
		maxBindings = 5000
	}
	if len(body.Bindings) > maxBindings {
		ErrorJSON(w, http.StatusBadRequest, "too many SQL bindings")
		return
	}

	if classifySQL(body.SQL) == "read" {
		h.execRead(w, r, name, body.SQL, body.Bindings)
	} else {
		h.execWrite(w, r, name, body.SQL, body.Bindings)
	}
}

func (h *ExecHandler) execRead(w http.ResponseWriter, r *http.Request, name, sqlStr string, bindings []any) {
	sqlDB, err := h.pool.Get(name)
	if err != nil {
		h.tel.IncError()
		ErrorJSON(w, http.StatusInternalServerError, "failed to open database")
		return
	}
	start := time.Now()
	rows, err := sqlDB.QueryContext(r.Context(), sqlStr, anySliceToDriverValues(bindings)...)
	if err != nil {
		h.tel.IncError()
		ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	defer rows.Close()

	headers, rowData, rowsRead, err := scanRows(rows)
	if err != nil {
		h.tel.IncError()
		ErrorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	elapsed := time.Since(start).Milliseconds()
	h.tel.IncRead(elapsed)
	writeJSON(w, http.StatusOK, map[string]any{
		"headers": headers,
		"rows":    rowData,
		"stat": map[string]any{
			"rowsRead":        rowsRead,
			"queryDurationMs": elapsed,
		},
	})
}

func (h *ExecHandler) execWrite(w http.ResponseWriter, r *http.Request, name, sqlStr string, bindings []any) {
	sqlDB, err := h.pool.Get(name)
	if err != nil {
		ErrorJSON(w, http.StatusInternalServerError, "failed to open database")
		return
	}

	// Result variables captured by the closure.
	var (
		headers         []colHeader
		rowData         []map[string]any
		rowsRead        int
		rowsAffected    int64
		lastInsertRowid int64
		isReader        bool
		elapsed         int64
	)

	qErr := h.queue.Enqueue(r.Context(), name, func() error {
		start := time.Now()
		args := anySliceToDriverValues(bindings)

		// Try Query first — handles RETURNING clauses and SELECT-like writes.
		rows, queryErr := sqlDB.QueryContext(r.Context(), sqlStr, args...)
		if queryErr == nil {
			cols, _ := rows.Columns()
			if len(cols) > 0 {
				isReader = true
				var scanErr error
				headers, rowData, rowsRead, scanErr = scanRows(rows)
				rows.Close()
				elapsed = time.Since(start).Milliseconds()
				return scanErr
			}
			rows.Close()
		}

		// Pure write: INSERT / UPDATE / DELETE without RETURNING.
		res, execErr := sqlDB.ExecContext(r.Context(), sqlStr, args...)
		if execErr != nil {
			return execErr
		}
		rowsAffected, _ = res.RowsAffected()
		lastInsertRowid, _ = res.LastInsertId()
		elapsed = time.Since(start).Milliseconds()
		return nil
	})

	if qErr != nil {
		log.Error().Err(qErr).Str("db", name).Msg("[exec] write queue error")
		h.tel.IncError()
		ErrorJSON(w, http.StatusBadRequest, qErr.Error())
		return
	}

	// Keep size_bytes in the registry up-to-date after every write (fire-and-forget).
	go func() {
		size := sysutil.FileSizeBytes(sysutil.DBPath(h.cfg.DataPath, name))
		if err := h.registry.UpdateDatabaseSize(name, size); err != nil {
			log.Warn().Err(err).Str("db", name).Msg("[exec] failed to update size_bytes")
		}
	}()

	if isReader {
		h.tel.IncRead(elapsed)
		writeJSON(w, http.StatusOK, map[string]any{
			"headers": headers,
			"rows":    rowData,
			"stat": map[string]any{
				"rowsRead":        rowsRead,
				"queryDurationMs": elapsed,
			},
		})
	} else {
		h.tel.IncWrite(elapsed)
		writeJSON(w, http.StatusOK, map[string]any{
			"rowsAffected":    rowsAffected,
			"lastInsertRowid": lastInsertRowid,
			"stat": map[string]any{
				"queryDurationMs": elapsed,
			},
		})
	}
}
