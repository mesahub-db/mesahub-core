# mesahub-core

The self-hosted SQLite service at the heart of MesaHUB.

Each database gets its own `.db` file on a persistent volume. A Go HTTP server exposes a REST + SQL API; a Next.js admin studio lets you browse and query databases in a browser.

## What you get

- Database registry (`databases`, `buckets`, `api_keys` — all in `store.db`)
- Per-database SQL query & exec endpoints with a write queue
- PostgREST-style auto-REST layer for any table
- File storage with presigned URL support and Caddy X-Sendfile delivery
- Volume-aware guardrails — DB creation blocked above a configurable usage threshold
- Admin studio UI for table browsing, query execution, and file management
- Health, metrics, and audit endpoints

## Deploy to Railway

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/deploy/sqlite-hub)

The Railway template provisions the service from the prebuilt image (`ghcr.io/0xdps/mesahub-core:latest`) and prompts you to set the required secrets.

**After deploying:**
1. In the service settings, add a **volume mounted at `/data`** — this is where all `.db` files and file blobs are stored. Without it, data is lost on every deploy.
2. Set the required environment variables (the template pre-fills names; you supply values).

## Self-hosted with Docker

### Prerequisites

- Docker (with Compose v2)
- `openssl` for generating secrets

### 1. Generate secrets

```bash
for v in ADMIN_TOKEN SESSION_SECRET FILE_TOKEN_SIGNING_SECRET FILE_URL_SIGNING_SECRET; do
  echo "$v=$(openssl rand -hex 32)"
done
```

### 2. Create your env file

```bash
cp .env.example .env
# Paste the secrets from step 1 into .env
```

Minimum required values in `.env`:

```dotenv
ADMIN_TOKEN=<generated>
SESSION_SECRET=<generated>
FILE_TOKEN_SIGNING_SECRET=<generated>
FILE_URL_SIGNING_SECRET=<generated>
DATA_PATH=/data
```

### 3. Run

```bash
docker compose --profile prod up -d
```

The service starts on port **8080** by default. Override with `PORT=` in your env.

Admin studio: `http://localhost:8080`  
API: `http://localhost:8080/api/`

### Local development

```bash
just doctor   # preflight: Docker, just, node, .env present
just dev      # build + start dev container (hot-reload admin UI)
just down     # stop
just smoke    # health + auth check against running service
```

Dev mode mounts the admin source directory and starts Next.js with hot reload.

## Environment variables

**Required:**

| Variable | Description | Generate with |
|---|---|---|
| `ADMIN_TOKEN` | Admin API bearer token (min 8 chars) | `openssl rand -hex 16` |
| `SESSION_SECRET` | Admin studio session signing key (min 32 chars) | `openssl rand -hex 32` |
| `FILE_TOKEN_SIGNING_SECRET` | Signs file access tokens | `openssl rand -hex 32` |
| `FILE_URL_SIGNING_SECRET` | Signs presigned download URLs | `openssl rand -hex 32` |

**Optional:**

| Variable | Default | Notes |
|---|---|---|
| `DATA_PATH` | `/data` | Must match the volume mount path |
| `PORT` | `80` (container) | External port Caddy listens on; Railway sets this automatically |
| `REDIS_URL` | — | When set, enables Redis-backed API key cache and sessions |
| `MAX_VOLUME_USAGE_PERCENT` | `85` | Block new DB creation above this disk usage % |
| `MAX_SQL_LENGTH` | `100000` | Max SQL statement length in characters |
| `MAX_SQL_BINDINGS` | `5000` | Max bound parameters per query |
| `FILE_MAX_SIZE_BYTES` | `104857600` | 100 MB per file |
| `FILE_MAX_FILES_PER_DB` | `10000` | File count cap per database |
| `FILE_MAX_STORAGE_PER_DB_BYTES` | `5368709120` | 5 GB storage cap per database |
| `FILE_PRESIGN_DEFAULT_TTL_SECONDS` | `900` | Default presigned URL lifetime (15 min) |
| `FILE_PRESIGN_MAX_TTL_SECONDS` | `86400` | Max presigned URL lifetime (24 h) |
| `CORS_ALLOWED_ORIGINS` | — | Comma-separated origins for cross-origin browser requests |
| `ENABLE_SYSTEM_DB_WRITE` | `false` | Allow admin `exec` against `store.db` via `/api/system/db/{name}/exec` |
| `LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error` |
| `MAINTENANCE_MODE` | `false` | Redirect all traffic to `/maintenance` and return 503 for API calls |

## API reference

**Auth** — all routes except `/api/health` and `/api/version` require:  
`Authorization: Bearer <ADMIN_TOKEN>` (full admin access) **or** `Authorization: Bearer shs_<key>` (scoped API key).

### Databases

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/db` | List active databases |
| `POST` | `/api/db` | Create a database |
| `GET` | `/api/db/{name}` | Get database metadata |
| `PATCH` | `/api/db/{name}` | Update metadata |
| `DELETE` | `/api/db/{name}` | Soft-delete a database |
| `GET` | `/api/db/deleted` | List soft-deleted databases |
| `POST` | `/api/db/deleted/{name}` | Restore or hard-delete |

### Query & exec

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/db/{name}/query` | Execute read-only SQL |
| `POST` | `/api/db/{name}/exec` | Execute write SQL (queued) |
| `POST` | `/api/db/{name}/import` | Import a `.db` file |
| `POST` | `/api/db/{name}/export` | Export as `.db` file |

### Auto-REST

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/db/{name}/rest/{table}` | Select rows |
| `POST` | `/api/db/{name}/rest/{table}` | Insert row |
| `PATCH` | `/api/db/{name}/rest/{table}` | Update rows |
| `DELETE` | `/api/db/{name}/rest/{table}` | Delete rows |

### Files

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/db/{name}/files` | List files |
| `POST` | `/api/db/{name}/files` | Upload a file |
| `GET` | `/api/db/{name}/files/{id}` | Download / stream |
| `GET` | `/api/db/{name}/files/{id}/meta` | File metadata |
| `POST` | `/api/db/{name}/files/{id}/presign` | Create a presigned URL |
| `DELETE` | `/api/db/{name}/files/{id}` | Delete a file |
| `POST` | `/api/db/{name}/files/bulk-delete` | Delete multiple files |

### Buckets (admin-only)

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/buckets` | List buckets |
| `POST` | `/api/buckets` | Create a bucket |
| `DELETE` | `/api/buckets/{name}` | Delete a bucket |
| `GET` | `/api/buckets/{name}/files` | List bucket files |
| `POST` | `/api/buckets/{name}/files` | Upload to bucket |
| `GET` | `/api/buckets/{name}/files/{id}` | Download from bucket |
| `DELETE` | `/api/buckets/{name}/files/{id}` | Delete from bucket |

### API keys (admin-only)

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/apikeys` | Create an API key |
| `GET` | `/api/apikeys` | List API keys |
| `DELETE` | `/api/apikeys/{id}` | Revoke an API key |

### System

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/health` | Health check (no auth) |
| `GET` | `/api/version` | Server version (no auth) |
| `GET` | `/api/metrics` | Storage and request metrics |
| `GET` | `/api/system/dbs` | List system databases |
| `POST` | `/api/system/db/{name}/query` | Query a system database |
| `POST` | `/api/system/db/{name}/exec` | Exec on a system DB (`ENABLE_SYSTEM_DB_WRITE=true` required) |
| `POST` | `/api/maintenance/cleanup` | Purge soft-deleted records |

## License

[Elastic License 2.0 (ELv2)](./LICENSE) — free to self-host and modify; you may not offer it as a managed service competing with MesaHUB.
