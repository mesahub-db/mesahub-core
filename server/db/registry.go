// Package db — registry.go manages store.db, the single source of truth
// for all user databases on this instance.
package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/0xdps/mesahub-core/cache"
)

// Registry wraps the shared store.db connection.
type Registry struct {
	db       *sql.DB
	dataPath string
	cache    cache.Client // may be nil — set via SetCache
}

// DBRecord mirrors the `databases` table row.
type DBRecord struct {
	ID           string
	Name         string // user-given display name
	Slug         string // generated template-internal filename
	Description  sql.NullString
	Owner        string
	Source       string
	InstanceID   sql.NullString
	Status       string
	SizeBytes    int64
	OriginalSlug sql.NullString // tracks pre-delete slug so restore can rename the file back
	CreatedAt    string
	UpdatedAt    sql.NullString
	DeletedAt    sql.NullString
}

// BucketRecord mirrors the `buckets` table row.
type BucketRecord struct {
	ID             string
	Name           string // user-given display name
	Slug           string // generated template-internal filename
	Description    sql.NullString
	Owner          string
	Source         string
	InstanceID     sql.NullString
	Status         string
	SizeBytes      int64
	APIKeyID       sql.NullString // references api_keys.id for the auto-generated bucket key
	StorageBackend string         // "local" | "s3" | "r2" — which blob store backs this bucket
	CreatedAt      string
	UpdatedAt      sql.NullString
	DeletedAt      sql.NullString
}

// AuditMetrics holds aggregated audit stats.
type AuditMetrics struct {
	TotalEvents   int64
	EventsLast24h int64
	ByTypeLast24h map[string]int64
}

const registrySchema = `
CREATE TABLE IF NOT EXISTS api_keys (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  key_hash     TEXT NOT NULL UNIQUE,
  scopes       TEXT NOT NULL DEFAULT '["all:w"]',
  owner        TEXT NOT NULL DEFAULT 'admin',
  key_type     TEXT NOT NULL DEFAULT 'admin',
  expires_at   TEXT,
  status       TEXT NOT NULL DEFAULT 'active',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS databases (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  slug          TEXT NOT NULL UNIQUE,
  description   TEXT,
  owner         TEXT NOT NULL DEFAULT 'admin',
  source        TEXT NOT NULL DEFAULT 'template',
  instance_id   TEXT,
  status        TEXT NOT NULL DEFAULT 'active',
  size_bytes    INTEGER NOT NULL DEFAULT 0,
  original_slug TEXT,
  created_at    TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at    TEXT,
  deleted_at    TEXT,
  UNIQUE(owner, slug)
);
CREATE INDEX IF NOT EXISTS idx_databases_owner ON databases(owner);

CREATE TABLE IF NOT EXISTS buckets (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL,
  slug             TEXT NOT NULL UNIQUE,
  description      TEXT,
  owner            TEXT NOT NULL DEFAULT 'admin',
  source           TEXT NOT NULL DEFAULT 'template',
  instance_id      TEXT,
  status           TEXT NOT NULL DEFAULT 'active',
  size_bytes       INTEGER NOT NULL DEFAULT 0,
  api_key_id       TEXT,
  storage_backend  TEXT NOT NULL DEFAULT 'local',
  created_at       TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at       TEXT,
  deleted_at       TEXT,
  UNIQUE(owner, slug)
);
CREATE INDEX IF NOT EXISTS idx_buckets_owner ON buckets(owner);

CREATE TABLE IF NOT EXISTS file_token_revocations (
  token_id   TEXT PRIMARY KEY,
  db_name    TEXT NOT NULL,
  expires_at DATETIME NOT NULL,
  reason     TEXT,
  revoked_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_ftr_db_name   ON file_token_revocations(db_name);
CREATE INDEX IF NOT EXISTS idx_ftr_expires   ON file_token_revocations(expires_at);

CREATE TABLE IF NOT EXISTS audit_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  event_type TEXT NOT NULL,
  db_name    TEXT,
  actor      TEXT,
  metadata   TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_audit_created   ON audit_events(created_at);
CREATE INDEX IF NOT EXISTS idx_audit_db_name   ON audit_events(db_name);
CREATE INDEX IF NOT EXISTS idx_audit_event_type ON audit_events(event_type);

CREATE TABLE IF NOT EXISTS schema_migrations (
  name       TEXT PRIMARY KEY,
  applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

// OpenRegistry opens (or creates) registry.db at dataPath and applies the
// schema. It is idempotent — safe to call on every startup.
func OpenRegistry(dataPath string) (*Registry, error) {
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		return nil, fmt.Errorf("registry: mkdir %s: %w", dataPath, err)
	}
	dbPath := filepath.Join(dataPath, "store.db")
	db, err := open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("registry: open: %w", err)
	}
	if _, err := db.Exec(registrySchema); err != nil {
		return nil, fmt.Errorf("registry: schema: %w", err)
	}
	reg := &Registry{db: db, dataPath: dataPath}
	if err := reg.migrateUnifiedSchema(); err != nil {
		return nil, fmt.Errorf("registry: migrate: %w", err)
	}
	if err := reg.migrateRegistrySchema(); err != nil {
		return nil, fmt.Errorf("registry: migrate schema: %w", err)
	}
	return reg, nil
}

// migrateUnifiedSchema rebuilds `databases` and `buckets` from the old column
// layout (display_name / integer PK) to the new unified layout.
func (r *Registry) migrateUnifiedSchema() error {
	return r.RunOnce("unified-schema-v1", func() error {
		// ── databases ───────────────────────────────────────────────────────
		// Old layout: id INTEGER PK, name=slug_filename, slug=uuid, display_name=user_name, original_name
		// Detect by checking if `display_name` column still exists.
		var hasDisplayName int
		_ = r.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('databases') WHERE name = 'display_name'`,
		).Scan(&hasDisplayName)

		if hasDisplayName > 0 {
			_, err := r.db.Exec(`
				CREATE TABLE IF NOT EXISTS databases_new (
					id            TEXT PRIMARY KEY,
					name          TEXT NOT NULL,
					slug          TEXT NOT NULL UNIQUE,
					description   TEXT,
					owner         TEXT NOT NULL DEFAULT 'admin',
					source        TEXT NOT NULL DEFAULT 'template',
					instance_id   TEXT,
					status        TEXT NOT NULL DEFAULT 'active',
					size_bytes    INTEGER NOT NULL DEFAULT 0,
					original_slug TEXT,
					created_at    TEXT NOT NULL DEFAULT (datetime('now')),
					updated_at    TEXT,
					deleted_at    TEXT,
					UNIQUE(owner, slug)
				);
				INSERT INTO databases_new
					SELECT
						COALESCE(slug, lower(hex(randomblob(16)))),
						COALESCE(NULLIF(display_name,''), name),
						name,
						description,
						owner,
						source,
						instance_id,
						status,
						size_bytes,
						original_name,
						created_at,
						updated_at,
						deleted_at
					FROM databases;
				DROP TABLE databases;
				ALTER TABLE databases_new RENAME TO databases;
				CREATE INDEX IF NOT EXISTS idx_databases_owner ON databases(owner);
			`)
			if err != nil {
				return fmt.Errorf("databases migration: %w", err)
			}
		}

		// ── buckets ─────────────────────────────────────────────────────────
		// Old layout: id TEXT PK, name=slug_filename, display_name=user_name, slug=dup_uuid
		var bucketHasDisplayName int
		_ = r.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('buckets') WHERE name = 'display_name'`,
		).Scan(&bucketHasDisplayName)

		if bucketHasDisplayName > 0 {
			_, err := r.db.Exec(`
				CREATE TABLE IF NOT EXISTS buckets_new (
					id          TEXT PRIMARY KEY,
					name        TEXT NOT NULL,
					slug        TEXT NOT NULL UNIQUE,
					description TEXT,
					owner       TEXT NOT NULL DEFAULT 'admin',
					source      TEXT NOT NULL DEFAULT 'template',
					instance_id TEXT,
					status      TEXT NOT NULL DEFAULT 'active',
					size_bytes  INTEGER NOT NULL DEFAULT 0,
					created_at  TEXT NOT NULL DEFAULT (datetime('now')),
					updated_at  TEXT,
					deleted_at  TEXT,
					UNIQUE(owner, slug)
				);
				INSERT INTO buckets_new
					SELECT
						id,
						display_name,
						name,
						description,
						owner,
						source,
						instance_id,
						status,
						size_bytes,
						created_at,
						NULL,
						NULL
					FROM buckets;
				DROP TABLE buckets;
				ALTER TABLE buckets_new RENAME TO buckets;
				CREATE INDEX IF NOT EXISTS idx_buckets_owner ON buckets(owner);
			`)
			if err != nil {
				return fmt.Errorf("buckets migration: %w", err)
			}
		}

		return nil
	})
}

// migrateRegistrySchema adds new columns to existing tables without recreating them.
func (r *Registry) migrateRegistrySchema() error {
	if err := r.RunOnce("registry-schema-v2", func() error {
		var hasAPIKeyID int
		_ = r.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('buckets') WHERE name = 'api_key_id'`,
		).Scan(&hasAPIKeyID)
		if hasAPIKeyID == 0 {
			_, err := r.db.Exec(`ALTER TABLE buckets ADD COLUMN api_key_id TEXT`)
			if err != nil {
				return fmt.Errorf("add api_key_id to buckets: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	return r.RunOnce("registry-schema-v3", func() error {
		var hasStorageBackend int
		_ = r.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('buckets') WHERE name = 'storage_backend'`,
		).Scan(&hasStorageBackend)
		if hasStorageBackend == 0 {
			_, err := r.db.Exec(`ALTER TABLE buckets ADD COLUMN storage_backend TEXT NOT NULL DEFAULT 'local'`)
			if err != nil {
				return fmt.Errorf("add storage_backend to buckets: %w", err)
			}
		}
		return nil
	})
}

// Close closes the underlying registry database connection.
func (r *Registry) Close() error { return r.db.Close() }

// DB returns the underlying *sql.DB for the registry store. Used by
// StoreHandler to serve raw-SQL access to store.db over HTTP.
func (r *Registry) DB() *sql.DB { return r.db }

// SetCache wires a cache.Client into the registry for list-operation caching.
// Must be called before any ListDatabasesByOwner / ListBucketsByOwner calls
// that should benefit from caching. Safe to call multiple times.
func (r *Registry) SetCache(c cache.Client) { r.cache = c }

// dbListKey returns the Redis key for a user's database list.
func dbListKey(owner string) string { return "sh:dbs:" + owner }

// bucketListKey returns the Redis key for a user's bucket list.
func bucketListKey(owner string) string { return "sh:buckets:" + owner }

const listCacheTTL = 5 * time.Minute

// invalidateDBList removes the cached database list for owner, if any.
func (r *Registry) invalidateDBList(owner string) {
	if r.cache != nil {
		_ = r.cache.DeleteKey(context.Background(), dbListKey(owner))
	}
}

// invalidateBucketList removes the cached bucket list for owner, if any.
func (r *Registry) invalidateBucketList(owner string) {
	if r.cache != nil {
		_ = r.cache.DeleteKey(context.Background(), bucketListKey(owner))
	}
}

// RunOnce executes fn exactly once, identified by name. If name is already
// recorded in schema_migrations, fn is skipped entirely. On success, the name
// is inserted so subsequent calls are no-ops.
func (r *Registry) RunOnce(name string, fn func() error) error {
	var applied string
	err := r.db.QueryRow(`SELECT name FROM schema_migrations WHERE name = ?`, name).Scan(&applied)
	if err == nil {
		return nil // already ran
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("schema_migrations lookup: %w", err)
	}
	if err := fn(); err != nil {
		return err
	}
	_, err = r.db.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, name)
	return err
}

// ── CRUD ─────────────────────────────────────────────────────────────────────

const dbColumns = `id, name, slug, description, owner, source, instance_id, status, size_bytes, original_slug, created_at, updated_at, deleted_at`

// ListDatabases returns all non-deleted databases ordered by created_at desc.
func (r *Registry) ListDatabases() ([]DBRecord, error) {
	rows, err := r.db.Query(
		`SELECT ` + dbColumns + ` FROM databases WHERE status != 'deleted' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDBRecords(rows)
}

// GetDatabase returns the database record by slug (template-internal filename).
func (r *Registry) GetDatabase(slug string) (*DBRecord, error) {
	row := r.db.QueryRow(`SELECT `+dbColumns+` FROM databases WHERE slug = ?`, slug)
	rec, err := scanDBRecord(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// GetDatabaseByID returns the database record by its UUID primary key.
func (r *Registry) GetDatabaseByID(id string) (*DBRecord, error) {
	row := r.db.QueryRow(`SELECT `+dbColumns+` FROM databases WHERE id = ?`, id)
	rec, err := scanDBRecord(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// InsertDatabase creates a new database record.
// id is a UUID; name is the user-given label; slug is the template-internal filename.
func (r *Registry) InsertDatabase(id, name, slug, owner, source string, instanceID *string, description *string) (*DBRecord, error) {
	_, err := r.db.Exec(
		`INSERT INTO databases (id, name, slug, owner, source, instance_id, description) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, slug, owner, source, strPtr(instanceID), strPtr(description))
	if err != nil {
		return nil, err
	}
	r.invalidateDBList(owner)
	return r.GetDatabase(slug)
}

// SetDatabaseStatus updates the status field to 'active' or 'inactive'.
func (r *Registry) SetDatabaseStatus(slug, status string) error {
	rec, _ := r.GetDatabase(slug)
	_, err := r.db.Exec(`UPDATE databases SET status = ? WHERE slug = ?`, status, slug)
	if err == nil && rec != nil {
		r.invalidateDBList(rec.Owner)
	}
	return err
}

// SoftDeleteDatabase renames the DB file and marks the record as deleted.
// The current slug is stored in original_slug so Restore can rename the file back.
func (r *Registry) SoftDeleteDatabase(pool *Pool, slug string) error {
	epoch := time.Now().Unix()
	newSlug := fmt.Sprintf("%s-%d", slug, epoch)

	// Rename the DB file (and WAL/SHM sidecars) if they exist.
	oldBase := filepath.Join(r.dataPath, slug+".db")
	newBase := filepath.Join(r.dataPath, newSlug+".db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		old := oldBase + suffix
		nw := newBase + suffix
		if _, statErr := os.Stat(old); statErr == nil {
			if err := os.Rename(old, nw); err != nil {
				return fmt.Errorf("rename %s: %w", old, err)
			}
		}
	}

	// Close pool connection so WAL is checkpointed before rename.
	pool.mu.Lock()
	if db, ok := pool.conns[slug]; ok {
		_ = db.Close()
		delete(pool.conns, slug)
	}
	pool.mu.Unlock()

	rec, _ := r.GetDatabase(slug)
	_, err := r.db.Exec(
		`UPDATE databases
		 SET slug = ?, original_slug = ?, status = 'deleted', deleted_at = datetime('now')
		 WHERE slug = ?`,
		newSlug, slug, slug)
	if err == nil && rec != nil {
		r.invalidateDBList(rec.Owner)
	}
	return err
}

// ListDatabasesByOwner returns all non-deleted databases for a given owner.
// Results are cached in Redis (if available) for listCacheTTL. The cache is
// set on read and invalidated on any mutation that changes the list.
func (r *Registry) ListDatabasesByOwner(owner string) ([]DBRecord, error) {
	if r.cache != nil {
		var cached []DBRecord
		hit, err := r.cache.GetJSON(context.Background(), dbListKey(owner), &cached)
		if err == nil && hit {
			return cached, nil
		}
	}

	rows, err := r.db.Query(
		`SELECT `+dbColumns+` FROM databases WHERE owner = ? AND status != 'deleted' ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result, err := scanDBRecords(rows)
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		_ = r.cache.SetJSON(context.Background(), dbListKey(owner), result, listCacheTTL)
	}
	return result, nil
}

// ListBucketsByOwner returns all active buckets for a given owner.
// Results are cached in Redis (if available) for listCacheTTL.
func (r *Registry) ListBucketsByOwner(owner string) ([]BucketRecord, error) {
	if r.cache != nil {
		var cached []BucketRecord
		hit, err := r.cache.GetJSON(context.Background(), bucketListKey(owner), &cached)
		if err == nil && hit {
			return cached, nil
		}
	}

	rows, err := r.db.Query(
		`SELECT `+bucketColumns+` FROM buckets WHERE owner = ? AND status != 'deleted' ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result, err := scanBucketRecords(rows)
	if err != nil {
		return nil, err
	}

	if r.cache != nil {
		_ = r.cache.SetJSON(context.Background(), bucketListKey(owner), result, listCacheTTL)
	}
	return result, nil
}

// ListAPIKeysByOwner returns all active API keys for a given owner.
func (r *Registry) ListAPIKeysByOwner(owner string) ([]APIKeyRecord, error) {
	rows, err := r.db.Query(
		`SELECT id, name, key_hash, scopes, owner, key_type,
		        expires_at, status, last_used_at, created_at
		 FROM api_keys WHERE owner = ? AND status = 'active' ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAPIKeyRecords(rows)
}

// ListDeletedDatabases returns all soft-deleted databases.
func (r *Registry) ListDeletedDatabases() ([]DBRecord, error) {
	rows, err := r.db.Query(
		`SELECT ` + dbColumns + ` FROM databases WHERE status = 'deleted' ORDER BY deleted_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDBRecords(rows)
}

// RestoreDatabase un-deletes a soft-deleted database.
func (r *Registry) RestoreDatabase(pool *Pool, deletedSlug string) (*DBRecord, error) {
	rec, err := r.GetDatabase(deletedSlug)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != "deleted" || !rec.OriginalSlug.Valid {
		return nil, fmt.Errorf("database %q not found or not in deleted state", deletedSlug)
	}

	originalSlug := rec.OriginalSlug.String
	oldBase := filepath.Join(r.dataPath, deletedSlug+".db")
	newBase := filepath.Join(r.dataPath, originalSlug+".db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		old := oldBase + suffix
		nw := newBase + suffix
		if _, statErr := os.Stat(old); statErr == nil {
			if err := os.Rename(old, nw); err != nil {
				return nil, fmt.Errorf("rename %s: %w", old, err)
			}
		}
	}

	_, err = r.db.Exec(
		`UPDATE databases SET slug = ?, original_slug = NULL, status = 'active', deleted_at = NULL WHERE slug = ?`,
		originalSlug, deletedSlug)
	if err != nil {
		return nil, err
	}
	r.invalidateDBList(rec.Owner)
	return r.GetDatabase(originalSlug)
}

// HardDeleteDatabase permanently removes a soft-deleted database.
func (r *Registry) HardDeleteDatabase(deletedSlug string) error {
	rec, err := r.GetDatabase(deletedSlug)
	if err != nil {
		return err
	}
	if rec == nil || rec.Status != "deleted" {
		return fmt.Errorf("database %q not found or not in deleted state", deletedSlug)
	}

	dbPath := filepath.Join(r.dataPath, deletedSlug+".db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		f := dbPath + suffix
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", f, err)
		}
	}

	_, err = r.db.Exec(`DELETE FROM databases WHERE slug = ?`, deletedSlug)
	if err == nil {
		r.invalidateDBList(rec.Owner)
	}
	return err
}

// ── File token revocations ────────────────────────────────────────────────────

// RevokeFileToken inserts or upserts a revocation record for a file access token.
func (r *Registry) RevokeFileToken(tokenID, dbName, expiresAt string, reason *string) error {
	_, err := r.db.Exec(
		`INSERT INTO file_token_revocations (token_id, db_name, expires_at, reason)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(token_id) DO UPDATE SET
		   db_name = excluded.db_name,
		   expires_at = excluded.expires_at,
		   reason = excluded.reason,
		   revoked_at = CURRENT_TIMESTAMP`,
		tokenID, dbName, expiresAt, strPtr(reason))
	return err
}

// IsFileTokenRevoked returns true if the token has an active revocation.
func (r *Registry) IsFileTokenRevoked(tokenID, dbName string) (bool, error) {
	var id string
	err := r.db.QueryRow(
		`SELECT token_id FROM file_token_revocations
		 WHERE token_id = ? AND db_name = ? AND datetime(expires_at) > datetime('now')
		 LIMIT 1`,
		tokenID, dbName).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CleanupExpiredTokenRevocations deletes expired revocation records.
func (r *Registry) CleanupExpiredTokenRevocations() (int64, error) {
	res, err := r.db.Exec(
		`DELETE FROM file_token_revocations WHERE datetime(expires_at) <= datetime('now')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ── Audit events ──────────────────────────────────────────────────────────────

// RecordAuditEvent appends an audit log entry.
func (r *Registry) RecordAuditEvent(eventType string, dbName, actor *string, metadata map[string]any) error {
	var metaStr *string
	if metadata != nil {
		b, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		s := string(b)
		metaStr = &s
	}
	_, err := r.db.Exec(
		`INSERT INTO audit_events (event_type, db_name, actor, metadata) VALUES (?, ?, ?, ?)`,
		eventType, strPtr(dbName), strPtr(actor), metaStr)
	return err
}

// GetAuditMetrics returns aggregate audit statistics.
func (r *Registry) GetAuditMetrics() (AuditMetrics, error) {
	var m AuditMetrics
	err := r.db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN datetime(created_at) >= datetime('now', '-1 day') THEN 1 ELSE 0 END), 0)
		 FROM audit_events`).Scan(&m.TotalEvents, &m.EventsLast24h)
	if err != nil {
		return m, err
	}

	rows, err := r.db.Query(
		`SELECT event_type, COUNT(*) FROM audit_events
		 WHERE datetime(created_at) >= datetime('now', '-1 day')
		 GROUP BY event_type`)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	m.ByTypeLast24h = map[string]int64{}
	for rows.Next() {
		var typ string
		var cnt int64
		if err := rows.Scan(&typ, &cnt); err != nil {
			return m, err
		}
		m.ByTypeLast24h[typ] = cnt
	}
	return m, rows.Err()
}

// ── API keys ──────────────────────────────────────────────────────────────────

// APIKeyRecord is a row from the api_keys table.
type APIKeyRecord struct {
	ID         string
	Name       string
	KeyHash    string
	Scopes     string
	Owner      string
	KeyType    string
	ExpiresAt  sql.NullString
	Status     string
	LastUsedAt sql.NullString
	CreatedAt  string
}

// InsertAPIKey inserts a new API key.
func (r *Registry) InsertAPIKey(id, name, keyHash, scopes, owner, keyType string, expiresAt *string) (*APIKeyRecord, error) {
	_, err := r.db.Exec(
		`INSERT INTO api_keys (id, name, key_hash, scopes, owner, key_type, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, name, keyHash, scopes, owner, keyType, strPtr(expiresAt))
	if err != nil {
		return nil, err
	}
	return r.GetAPIKeyByID(id)
}

// GetAPIKeyByID returns a key record by its ID.
func (r *Registry) GetAPIKeyByID(id string) (*APIKeyRecord, error) {
	row := r.db.QueryRow(
		`SELECT id, name, key_hash, scopes, owner, key_type,
		        expires_at, status, last_used_at, created_at
		 FROM api_keys WHERE id = ?`, id)
	return scanAPIKeyRecord(row)
}

// GetAPIKeyByHash returns a key record by its SHA-256 hash (used during auth).
func (r *Registry) GetAPIKeyByHash(hash string) (*APIKeyRecord, error) {
	row := r.db.QueryRow(
		`SELECT id, name, key_hash, scopes, owner, key_type,
		        expires_at, status, last_used_at, created_at
		 FROM api_keys WHERE key_hash = ? AND status = 'active'`, hash)
	rec, err := scanAPIKeyRecord(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// ListAPIKeys returns all active API key records (hash only — raw value is never stored).
func (r *Registry) ListAPIKeys() ([]APIKeyRecord, error) {
	rows, err := r.db.Query(
		`SELECT id, name, key_hash, scopes, owner, key_type,
		        expires_at, status, last_used_at, created_at
		 FROM api_keys WHERE status = 'active' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAPIKeyRecords(rows)
}

// RevokeAPIKey marks a key as revoked.
func (r *Registry) RevokeAPIKey(id string) error {
	_, err := r.db.Exec(`UPDATE api_keys SET status = 'revoked' WHERE id = ?`, id)
	return err
}

// TouchAPIKey updates last_used_at for a key.
func (r *Registry) TouchAPIKey(id string) error {
	_, err := r.db.Exec(
		`UPDATE api_keys SET last_used_at = datetime('now') WHERE id = ?`, id)
	return err
}

// ── Buckets ───────────────────────────────────────────────────────────────────

const bucketColumns = `id, name, slug, description, owner, source, instance_id, status, size_bytes, api_key_id, storage_backend, created_at, updated_at, deleted_at`

// InsertBucket creates a new bucket record and auto-generates a dedicated shk_
// API key scoped to this bucket. The raw key is returned once and never stored.
func (r *Registry) InsertBucket(id, name, slug, owner, source string, instanceID *string, description *string, storageBackend string) (*BucketRecord, string, error) {
	// Generate a bucket-scoped API key.
	rawKey, keyHash, err := generateBucketKey()
	if err != nil {
		return nil, "", fmt.Errorf("generate bucket key: %w", err)
	}
	keyID := uuid.New().String()
	scopeJSON := fmt.Sprintf(`["bucket:%s:w"]`, slug)

	tx, err := r.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.Exec(
		`INSERT INTO api_keys (id, name, key_hash, scopes, owner, key_type) VALUES (?, ?, ?, ?, ?, ?)`,
		keyID, "bucket-key:"+slug, keyHash, scopeJSON, owner, "bucket")
	if err != nil {
		return nil, "", err
	}
	if storageBackend == "" {
		storageBackend = "local"
	}
	_, err = tx.Exec(
		`INSERT INTO buckets (id, name, slug, owner, source, instance_id, description, api_key_id, storage_backend) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, slug, owner, source, strPtr(instanceID), strPtr(description), keyID, storageBackend)
	if err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}

	r.invalidateBucketList(owner)
	rec, err := r.GetBucket(slug)
	if err != nil {
		return nil, "", err
	}
	return rec, rawKey, nil
}

// generateBucketKey returns (rawKey, sha256hex, error).
// Raw key format: "shk_" + 32 random bytes base64url-encoded.
func generateBucketKey() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw := "shk_" + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), nil
}

// RevokeAPIKey marks a key as revoked.

// GetBucketByID returns a bucket record by its UUID primary key.
func (r *Registry) GetBucketByID(id string) (*BucketRecord, error) {
	row := r.db.QueryRow(`SELECT `+bucketColumns+` FROM buckets WHERE id = ?`, id)
	rec, err := scanBucketRecord(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// GetBucket returns a bucket record by slug (template-internal filename).
func (r *Registry) GetBucket(slug string) (*BucketRecord, error) {
	row := r.db.QueryRow(`SELECT `+bucketColumns+` FROM buckets WHERE slug = ?`, slug)
	rec, err := scanBucketRecord(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// ListBuckets returns all active buckets.
func (r *Registry) ListBuckets() ([]BucketRecord, error) {
	rows, err := r.db.Query(
		`SELECT ` + bucketColumns + ` FROM buckets WHERE status = 'active' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBucketRecords(rows)
}

// DeleteBucket marks a bucket as deleted.
func (r *Registry) DeleteBucket(slug string) error {
	rec, _ := r.GetBucket(slug)
	_, err := r.db.Exec(`UPDATE buckets SET status = 'deleted', deleted_at = datetime('now') WHERE slug = ?`, slug)
	if err == nil && rec != nil {
		r.invalidateBucketList(rec.Owner)
	}
	return err
}

// UpdateBucketSize increments the stored size_bytes for a bucket.
func (r *Registry) UpdateBucketSize(slug string, delta int64) error {
	_, err := r.db.Exec(
		`UPDATE buckets SET size_bytes = MAX(0, size_bytes + ?) WHERE slug = ?`, delta, slug)
	return err
}

// ── helpers ───────────────────────────────────────────────────────────────────

func strPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func scanDBRecord(row *sql.Row) (*DBRecord, error) {
	var r DBRecord
	// column order matches dbColumns const:
	// id, name, slug, description, owner, source, instance_id, status, size_bytes, original_slug, created_at, updated_at, deleted_at
	err := row.Scan(
		&r.ID, &r.Name, &r.Slug, &r.Description,
		&r.Owner, &r.Source, &r.InstanceID, &r.Status, &r.SizeBytes,
		&r.OriginalSlug, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func scanDBRecords(rows *sql.Rows) ([]DBRecord, error) {
	var out []DBRecord
	for rows.Next() {
		var r DBRecord
		if err := rows.Scan(
			&r.ID, &r.Name, &r.Slug, &r.Description,
			&r.Owner, &r.Source, &r.InstanceID, &r.Status, &r.SizeBytes,
			&r.OriginalSlug, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanAPIKeyRecord(row *sql.Row) (*APIKeyRecord, error) {
	var r APIKeyRecord
	err := row.Scan(&r.ID, &r.Name, &r.KeyHash, &r.Scopes,
		&r.Owner, &r.KeyType,
		&r.ExpiresAt, &r.Status, &r.LastUsedAt, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func scanAPIKeyRecords(rows *sql.Rows) ([]APIKeyRecord, error) {
	var out []APIKeyRecord
	for rows.Next() {
		var r APIKeyRecord
		if err := rows.Scan(&r.ID, &r.Name, &r.KeyHash, &r.Scopes,
			&r.Owner, &r.KeyType,
			&r.ExpiresAt, &r.Status, &r.LastUsedAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanBucketRecord(row *sql.Row) (*BucketRecord, error) {
	var r BucketRecord
	// column order matches bucketColumns const:
	// id, name, slug, description, owner, source, instance_id, status, size_bytes, api_key_id, storage_backend, created_at, updated_at, deleted_at
	err := row.Scan(
		&r.ID, &r.Name, &r.Slug, &r.Description,
		&r.Owner, &r.Source, &r.InstanceID,
		&r.Status, &r.SizeBytes, &r.APIKeyID, &r.StorageBackend, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func scanBucketRecords(rows *sql.Rows) ([]BucketRecord, error) {
	var out []BucketRecord
	for rows.Next() {
		var r BucketRecord
		if err := rows.Scan(
			&r.ID, &r.Name, &r.Slug, &r.Description,
			&r.Owner, &r.Source, &r.InstanceID,
			&r.Status, &r.SizeBytes, &r.APIKeyID, &r.StorageBackend, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
