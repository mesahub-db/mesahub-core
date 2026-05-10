# MesaHub Core — Copilot Instructions

## What this service is
The **core SQLite service library** — imported by `mesahub-cloud` and served as part of
the cloud binary. `core/` is **never deployed directly**; the cloud binary in `cloud/`
is the only deployable artifact.

It owns:
- The Go package (`server/`) that implements all SQLite APIs
- The Next.js admin UI (`admin/`) for browsing databases in a browser
- The `store.db` schema (registry of user databases, buckets, API keys)

---

## Roles & responsibilities

| Layer | Owns | Does NOT own |
|---|---|---|
| Go server | `store.db` schema, API handlers, auth, file storage | Billing logic, accounts.db, SaaS routing |
| Next.js admin | Database browser UI | Writing to store.db, managing users |

---

## Go server conventions (`server/`)

### Module path
`github.com/0xdps/mesahub-core`

### Package layout
| Package | Purpose |
|---|---|
| `config/` | Config struct + env var loading |
| `db/` | Pool (per-db connections), Registry (store.db) |
| `queue/` | Per-db serialised write queue |
| `files/` | File storage + metadata DB |
| `filetoken/` | HMAC-SHA256 file access tokens |
| `sysutil/` | Volume info, DB path helpers |
| `auth/` | Request authorisation (AdminStamper, RequireAdmin, AuthorizeDB) |
| `cache/` | Redis or off-mode caching |
| `handler/` | All HTTP handlers |
| `middleware/` | Chi middleware |
| `migrate/` | Schema migration helpers |
| `telemetry/` | Structured logging helpers |

### Router
Uses `github.com/go-chi/chi/v5`. All routes mount under `/api`.
Admin routes require `x-mesahub-admin: 1`, stamped by `AdminStamper` middleware
when a valid `Authorization: Bearer <ADMIN_TOKEN>` is present.

### Auth model
- **Admin** — `ADMIN_TOKEN` bearer → `x-mesahub-admin: 1` → full access
- **API keys** — `shs_` prefix, stored as SHA-256 hash in `api_keys.key_hash` in `store.db`
- **Service secrets** — `sv_` prefix; handled by cloud-layer middleware; never use `shs_` for these
- `auth.AuthorizeDB(r, cfg, rec)` returns `(int, string)` — `0` means authorised

### store.db schema ownership
**`db/registry.go` is the single source of truth for the entire `store.db` schema.**
- All `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS` statements live here
- The `migrations` slice contains additive `ALTER TABLE` statements for already-deployed instances
- Schema is applied before the first request

### store.db tables
| Table | Key columns | Notes |
|---|---|---|
| `databases` | `id` (TEXT PK — UUID, frontend identifier), `name` (user-visible display name), `slug` (generated internal filename), `owner` (user ID), `status`, `size_bytes` | `id` is the URL param; no integer PK; no `display_name` column |
| `buckets` | `id` (TEXT PK), `name` (user-visible), `slug` (internal filename), `owner`, `storage_backend`, `status` | |
| `api_keys` | `id`, `name`, `key_hash`, `key_type`, `scopes` (JSON), `owner` (user ID), `expires_at`, `status` | Column is `owner`, not `user_id` |
| `file_token_revocations` | `token_id`, `db_name`, `expires_at` | |
| `audit_events` | `id`, `event_type`, `db_name`, `actor`, `metadata` | |
| `schema_migrations` | `name`, `applied_at` | Tracks applied migration names |

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

## Relationship with `cloud/`

The cloud binary (`github.com/0xdps/mesahub-cloud`) imports this package and:
1. Mounts all core routes on its router
2. Adds SaaS-specific routes on top (accounts.db, UUID routing, dedicated sync, etc.)
3. Is the only thing that runs in production

When you change core, the cloud binary must be rebuilt. The `go.work` file in the
monorepo root links both modules so `cd cloud/server && go build ./...` picks up
local core changes automatically.

---

## Next.js admin UI conventions (`admin/`)

- Next.js 15, standard webpack — **no Turbopack**
- Dev: `pnpm dev` (from `core/admin/`)
- Reads databases through the cloud binary's REST API at `NEXT_PUBLIC_API_URL`
- Never opens SQLite files directly
- `GO_API_URL` env var (set by startup script) overrides the API URL for server-side calls

---

## Environment variables

| Variable | Used by | Purpose |
|---|---|---|
| `ADMIN_TOKEN` | Go server | Admin bearer token |
| `SESSION_SECRET` | Go server | Session signing |
| `DATA_PATH` | Go server | Persistent volume path (default `/data`) |
| `REDIS_URL` | Go server | Optional Redis cache |
| `NEXT_PUBLIC_API_URL` | Admin UI | Cloud binary public base URL |
| `GO_API_URL` | Admin UI (server-side) | Cloud binary internal URL (set by startup script) |

---

## Build

```bash
# Go package (from monorepo root — uses go.work)
cd core/server && go build ./...

# Admin UI (dev)
cd core/admin && pnpm dev
```
