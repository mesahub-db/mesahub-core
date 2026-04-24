# ─────────────────────────────────────────────────────────────────────────────
# Stage 1: Build the Next.js admin UI (standalone output)
# ─────────────────────────────────────────────────────────────────────────────
FROM node:24-alpine AS ui-builder

RUN apk add --no-cache python3 make g++

WORKDIR /app/admin
COPY core/admin/package.json core/admin/pnpm-lock.yaml ./
RUN corepack enable pnpm && pnpm install --frozen-lockfile

COPY core/admin/ .

# Env vars baked into the JS bundle at build time by Next.js
ARG NEXT_PUBLIC_ENABLE_FILE_STORAGE=true
ENV NEXT_PUBLIC_ENABLE_FILE_STORAGE=$NEXT_PUBLIC_ENABLE_FILE_STORAGE

RUN pnpm build

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2: Build the Go server binary
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS go-builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app/server
COPY core/server/go.mod core/server/go.sum ./
RUN go mod download

COPY core/server/ .
RUN CGO_ENABLED=1 GOOS=linux go build -o mesahub-server ./cmd/server

# ─────────────────────────────────────────────────────────────────────────────
# dashboard-dev — standalone Next.js dev server, no Go binary, no Caddy.
# Used by docker-compose.saas.yml / docker-compose.full.yml where the Go API
# runs as a separate container. Supports bind-mount HMR.
# ─────────────────────────────────────────────────────────────────────────────
FROM node:24-alpine AS dashboard-dev

RUN apk add --no-cache curl python3 make g++ && \
    corepack enable pnpm

WORKDIR /app
COPY core/admin/package.json core/admin/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY core/admin/ .

ENV NODE_ENV=development
ENV NEXT_TELEMETRY_DISABLED=1
ENV PORT=3000
ENV HOSTNAME=0.0.0.0

EXPOSE 3000

HEALTHCHECK --interval=15s --timeout=5s --start-period=60s --retries=5 \
  CMD curl -sf http://localhost:3000 || exit 1

# --webpack: MDX + Turbopack schema conflicts (same as template/dashboard dev)
CMD ["pnpm", "next", "dev", "--port", "3000", "--hostname", "0.0.0.0"]

# ─────────────────────────────────────────────────────────────────────────────
# Development stage — hot-reload via Next.js dev server
# Must come before the production stage so that `docker build` (and Railway)
# targets `production` by default (last stage wins).
# ─────────────────────────────────────────────────────────────────────────────
FROM caddy:2-alpine AS development

RUN apk add --no-cache \
    nodejs \
    npm \
    supervisor \
    curl \
    python3 \
    make \
    g++

WORKDIR /app
RUN mkdir -p /data/files/blobs

# ── Go binary ────────────────────────────────────────────────────────────────
COPY --from=go-builder /app/server/mesahub-server ./server/mesahub-server

COPY core/admin/package.json core/admin/pnpm-lock.yaml ./dashboard/
RUN npm install -g pnpm && cd dashboard && pnpm install

COPY core/admin/ ./dashboard/
COPY core/start.sh /app/start.sh
RUN chmod +x /app/start.sh

EXPOSE 80
EXPOSE 443

CMD ["/app/start.sh"]

# ─────────────────────────────────────────────────────────────────────────────
# Stage 3: Production image — Caddy + supervisord + Go binary + static Vite dist
# This is the last stage — Docker and Railway build this target by default.
# ─────────────────────────────────────────────────────────────────────────────
FROM caddy:2-alpine AS production

# Runtime deps: Node.js for Next.js standalone, supervisord, curl for health checks.
RUN apk add --no-cache \
    nodejs \
    supervisor \
    curl

WORKDIR /app

# Create data & blob directories
RUN mkdir -p /data/files/blobs

# ── Next.js standalone build ──────────────────────────────────────────────────
COPY --from=ui-builder /app/admin/.next/standalone ./dashboard/.next/standalone
COPY --from=ui-builder /app/admin/.next/static ./dashboard/.next/standalone/.next/static
COPY --from=ui-builder /app/admin/public ./dashboard/.next/standalone/public

# ── Go binary ────────────────────────────────────────────────────────────────
COPY --from=go-builder /app/server/mesahub-server ./server/mesahub-server

# ── supervisord config ───────────────────────────────────────────────────────
COPY core/supervisord.conf /etc/supervisor/conf.d/mesahub.conf

# ── Caddy startup script (generates Caddyfile dynamically) ──────────────────
COPY core/start.sh /app/start.sh
RUN chmod +x /app/start.sh

EXPOSE 80
EXPOSE 443

CMD ["/app/start.sh"]
