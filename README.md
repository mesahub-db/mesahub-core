# sqlite-hub

sqlite-hub is a centralized SQLite platform for internal workloads.

It gives each service its own database file, keeps everything on one persistent volume, and provides a secure web UI for browsing/querying data.

## Why sqlite-hub

- One place to manage all internal SQLite databases
- Built-in admin dashboard and SQL viewer
- Simple token-based admin auth
- Railway-friendly deployment model with persistent storage
- Minimal operational overhead

## What you get

- Database registry and metadata (`owner`, `description`, status)
- Health and metrics endpoints
- Read/query APIs per database
- Volume-aware guardrails for DB creation
- Studio UI for table browsing and query execution

## Quick start

```bash
git clone https://github.com/0xdps/mesahub-mono.git
cd sqlite-hub
npm install
cp .env.example .env
just doctor
just dev
```

App URL: [http://sqlite-hub.localhost:1355](http://sqlite-hub.localhost:1355)

## Runtime model

- Single compose file: `docker-compose.yml`
- Dev profile: `app-dev` (Docker random port + Portless alias)
- Prod profile: `app-prod` (fixed local port, platform `$PORT` in deployment)
- Shared bootstrap script: `start.sh`

Common commands:

```bash
just doctor      # preflight checks
just dev         # foreground dev with logs
just prod        # local production-like mode
just smoke       # health + login checks against running service
just down        # stop running services
just dev-clean   # remove stale containers/orphans
```

## Railway deployment

### 1. Create the service

Create a new Railway service from this repository. Railway will detect `railway.json` and use the Dockerfile build automatically.

### 2. Attach a persistent volume

In the service settings add a volume mounted at **`/data`**. All database files, the registry, and file blobs are stored here. Without a persistent volume data is lost on every deploy.

### 3. Set environment variables

**Required:**

| Variable | Description | How to generate |
|---|---|---|
| `ADMIN_TOKEN` | Admin API + dashboard auth token | `openssl rand -hex 32` |
| `SESSION_SECRET` | Iron-session cookie signing key (min 32 chars) | `openssl rand -hex 32` |
| `FILE_URL_SIGNING_SECRET` | Signs presigned file URLs | `openssl rand -hex 32` |
| `FILE_TOKEN_SIGNING_SECRET` | Signs file access tokens | `openssl rand -hex 32` |
| `NODE_ENV` | Must be `production` | literal `production` |

**Recommended:**

| Variable | Default | Notes |
|---|---|---|
| `DATA_PATH` | `/data` | Must match the volume mount path |
| `MAX_VOLUME_USAGE_PERCENT` | `85` | DB creation is blocked above this threshold |
| `HOSTNAME` | `::` | Required for Railway private networking (IPv6-first) |
| `ENABLE_FILE_STORAGE` | `true` | Set `false` to disable the file storage feature |
| `ENABLE_FILE_PROXY_DELIVERY` | `true` | Caddy X-Sendfile acceleration for downloads |
| `FILE_MAX_SIZE_BYTES` | `104857600` | 100 MB per file |
| `FILE_MAX_STORAGE_PER_DB_BYTES` | `5368709120` | 5 GB per database |

**Control plane integration** (only if pairing with the control plane service):

| Variable | Description |
|---|---|
| `ENABLE_CONTROL_DB` | Set `true` to auto-provision the `control` database on startup |
| `CONTROL_DB_SECRET` | Shared secret — must match `CONTROL_DB_SECRET` in the control plane service |
| `SQLITE_HUB_ADMIN_TOKEN` | Same value as `ADMIN_TOKEN` — used by the control plane to call admin routes |

### 4. Deploy

Push to your branch or trigger a manual deploy. Railway will:
1. Build the multi-stage Dockerfile (Go binary + Next.js standalone + Caddy)
2. Start the service with `/app/start.sh`
3. Health-check `GET /api/health` (120 s timeout to allow cold-start)
4. Mark the deploy healthy and route traffic

### Generating secrets

```bash
# Single command to generate all four secrets
for v in ADMIN_TOKEN SESSION_SECRET FILE_URL_SIGNING_SECRET FILE_TOKEN_SIGNING_SECRET; do
  echo "$v=$(openssl rand -hex 32)"
done
```

Paste the output directly into the Railway service variables UI.

## API overview

Databases:

- `GET /api/db` list active databases
- `POST /api/db` create/register a database
- `GET /api/db/:name` fetch metadata for one database
- `DELETE /api/db/:name` soft-delete a database

Queries:

- `POST /api/db/:name/query` execute read-only SQL

Files:

- `GET /api/db/:name/files` list files for a database
- `POST /api/db/:name/files` upload a file
- `GET /api/db/:name/files/:id` download/stream a file
- `GET /api/db/:name/files/:id/meta` read file metadata
- `POST /api/db/:name/files/:id/presign` create a browser-safe signed URL
- `DELETE /api/db/:name/files/:id` delete a file
- `POST /api/db/:name/files/bulk-delete` delete many files

System:

- `GET /api/health` service health
- `GET /api/metrics` storage and request metrics

All API routes except `/api/health` require admin authentication.

## Documentation

- [GUIDE.md](./GUIDE.md) - end-to-end setup guide: local dev, Railway deploy, first database, querying, file storage
- [docs/DEVELOPMENT.md](./docs/DEVELOPMENT.md) - full development and operations guide
- [docs/FILE_STORAGE.md](./docs/FILE_STORAGE.md) - file upload/download API, limits, and storage model
- [docs/PRD.md](./docs/PRD.md) - product requirements
- [docs/TECH.md](./docs/TECH.md) - implementation reference

## License

MIT
