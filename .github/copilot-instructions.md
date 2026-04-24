# mesahub core — Copilot Instructions

## What this service is
The **self-hosted SQLite service** deployed to Railway. It owns:
- The Go HTTP server (`server/`) that handles all API requests
- The Next.js admin UI (`admin/`) for browsing databases in a browser
- The persistent volume at `/data` that holds every `.db` file

---

## Roles & responsibilities

| Layer | Owns | Does NOT own |
|---|---|---|
| Go server | All `.db` files on disk, schema, auth, API | Billing logic, user-facing subscription UI |
| Next.js admin | Read-only database browser UI | Writing to store.db, managing users |

---

## Go server conventions (`server/`)

### Module path
`github.com/0xdps/mesahub-core`

### Package layout
| Package | Purpose |
|---|---|
| `internal/config` | Config struct + env var loading |
| `internal/db` | Pool (per-db connections), Registry (store.db) |
| `internal/queue` | Per-db serialised write queue |
| `internal/files` | File storage + metadata DB |
| `internal/filetoken` | HMAC-SHA256 file access tokens |
| `internal/sysutil` | Volume info, DB path helpers |
| `internal/auth` | Request authorisation |
| `internal/cache` | Redis or off-mode caching |
| `internal/handler` | All HTTP handlers |
| `internal/middleware` | Chi middleware (admin stamper, etc.) |

### Router
Uses `github.com/go-chi/chi/v5`. All routes mount under `/api`.
Admin routes require the `x-sqlite-hub-admin: 1` header, which is stamped by
`AdminStamper` middleware when a valid `Authorization: Bearer <ADMIN_TOKEN>` is
present.

### Auth model
- **Admin** — `ADMIN_TOKEN` bearer → `x-sqlite-hub-admin: 1` → full access
- **API keys** — `shs_` prefix, stored as SHA-256 hash in `api_keys` table of `store.db`
- **Service secrets** — `sv_` prefix, different code path; never use `shs_` for service secrets
- `auth.AuthorizeDB(r, cfg, rec)` returns `(int, string)` — `0` means authorised

### store.db schema ownership
**`db/registry.go` is the single source of truth for the entire `store.db` schema.**
- All `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS` statements live here
- The `migrations` slice contains additive `ALTER TABLE` statements for already-deployed instances
- Schema is guaranteed to exist before the first request

### store.db tables
| Table | Key columns | Notes |
|---|---|---|
| `databases` | `id` (INTEGER PK), `slug` (UUID), `name` (filename), `display_name`, `owner` (user ID), `status` | `slug` is the frontend identifier |
| `buckets` | `id` (TEXT PK), `name`, `display_name`, `owner`, `status` | |
| `api_keys` | `id`, `key_hash`, `scopes` (JSON), `owner` (user ID), `status` | Column is `owner`, not `user_id` |
| `file_token_revocations` | `jti`, `expires_at` | |
| `audit_events` | `id`, `db_name`, `event_type`, `actor` | |

### Database HTTP endpoints (exposed for dashboard)
| Endpoint | Access | Purpose |
|---|---|---|
| `POST /api/db/store/query` | Admin | SELECT queries against store.db |
| `POST /api/db/store/exec` | Admin | DML/DDL against store.db (via write queue) |

### Write queue
All mutations to a given database go through `queue.WriteQueue`. Never write to a
SQLite file from multiple goroutines without the queue.

### Error handling
- `ErrorJSON(w, status, msg)` for all HTTP error responses
- Fatal startup errors call `log.Fatal()` with zerolog
- Migration `ALTER TABLE` errors containing `"duplicate column"` or `"already exists"` are silently skipped; any other error is fatal

### Logging
Uses `github.com/rs/zerolog`. Structured fields only — no `fmt.Printf` in handler code.

---

## Next.js admin UI conventions (`admin/`)

- Next.js 15, standard webpack (no Turbopack)
- Dev: `next dev`
- Reads databases through the Go server's REST API at `NEXT_PUBLIC_API_URL`
- Never opens SQLite files directly

---

## Environment variables

| Variable | Used by | Purpose |
|---|---|---|
| `ADMIN_TOKEN` | Go server | Admin bearer token |
| `SESSION_SECRET` | Go server | Session signing |
| `DATA_PATH` | Go server | Persistent volume path (default `/data`) |
| `REDIS_URL` | Go server | Optional Redis cache |
| `NEXT_PUBLIC_API_URL` | Admin UI | Go server base URL |

---

## Build & run

```bash
# Go server
cd server && go build ./...
DATA_PATH=/data SESSION_SECRET=secret ADMIN_TOKEN=token ./server

# Dashboard (dev)
cd dashboard && pnpm dev   # uses --webpack
```
