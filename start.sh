#!/bin/sh
set -e

# PORT       — external port Caddy listens on (set by Railway, default 80)
# Go and Next.js internal ports are fixed.
PORT=${PORT:-80}
GO_PORT=3000
NEXTJS_PORT=3001

# Split-domain routing — set all three to enable named-host rules in control mode.
# DOMAIN          e.g. mesahub.app
# API_HOST        single subdomain prefix, e.g. api   → api.mesahub.app
# ADMIN_HOSTS     comma-separated prefixes,  e.g. admin,manage
#                  → admin.mesahub.app manage.mesahub.app
DOMAIN="${DOMAIN:-}"
API_HOST="${API_HOST:-}"
ADMIN_HOSTS="${ADMIN_HOSTS:-}"

# Build fully-qualified hostnames from the parts above.
API_FULL_HOSTNAME=""
if [ -n "$API_HOST" ] && [ -n "$DOMAIN" ]; then
    API_FULL_HOSTNAME="${API_HOST}.${DOMAIN}"
fi
ADMIN_HOST_LIST=""
if [ -n "$ADMIN_HOSTS" ] && [ -n "$DOMAIN" ]; then
    for _subdomain in $(echo "$ADMIN_HOSTS" | tr ',' ' '); do
        ADMIN_HOST_LIST="$ADMIN_HOST_LIST ${_subdomain}.${DOMAIN}"
    done
    ADMIN_HOST_LIST="${ADMIN_HOST_LIST# }"  # trim leading space
fi

echo "PORT=$PORT (Caddy)  Go=:$GO_PORT  Next.js=:$NEXTJS_PORT"

# ---------------------------------------------------------------------------
if [ "$NODE_ENV" = "development" ]; then
# ---------------------------------------------------------------------------
    echo "Starting SQLite Hub in DEVELOPMENT mode"
    mkdir -p /data /data/files/blobs

    echo "Starting Go server on :$GO_PORT ..."
    PORT=$GO_PORT /app/server/mesahub-server &
    GO_PID=$!

    echo "Starting Next.js dev server on :$NEXTJS_PORT ..."
    cd /app/dashboard && PORT=$NEXTJS_PORT pnpm next dev --port $NEXTJS_PORT --hostname 0.0.0.0 &
    NEXTJS_PID=$!

    echo "Waiting for Go server ..."
    max_attempts=30
    attempt=0
    until curl -sf http://localhost:$GO_PORT/api/health > /dev/null 2>&1; do
        attempt=$((attempt + 1))
        if [ $attempt -eq $max_attempts ]; then
            echo "Go server failed to start on port $GO_PORT"
            kill $GO_PID $NEXTJS_PID 2>/dev/null || true
            exit 1
        fi
        sleep 2
    done
    echo "Go server ready"

    echo "Waiting for Next.js dev server ..."
    max_attempts=60
    attempt=0
    until curl -sf http://localhost:$NEXTJS_PORT/api/health > /dev/null 2>&1; do
        attempt=$((attempt + 1))
        if [ $attempt -eq $max_attempts ]; then
            echo "Next.js dev server failed to start on port $NEXTJS_PORT"
            kill $GO_PID $NEXTJS_PID 2>/dev/null || true
            exit 1
        fi
        sleep 2
    done
    echo "Next.js dev server ready"

    cat > /tmp/Caddyfile <<EOF
{
    auto_https off
    admin off
}

:${PORT} {
    @dslash path_regexp dslash ^//(.*)$
    rewrite @dslash /{http.regexp.dslash.1}

    # SDK data-plane routes (shs_ API key, no session cookie) → Go directly.
    handle /v1/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/exec/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/query/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/rest/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/files/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/buckets/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/health {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/version {
        reverse_proxy localhost:${GO_PORT}
    }

    # Everything else (dashboard UI, auth, admin proxy routes) → Next.js dev server
    handle {
        reverse_proxy localhost:${NEXTJS_PORT}
    }
}
EOF

    caddy fmt --overwrite /tmp/Caddyfile
    caddy run --config /tmp/Caddyfile

# ---------------------------------------------------------------------------
else
# ---------------------------------------------------------------------------
    echo "Starting SQLite Hub in PRODUCTION mode"

    # Railway volumes mount asynchronously — wait until /data is writable
    # before starting the Go server (which opens SQLite files there).
    DATA_PATH="${DATA_PATH:-/data}"
    echo "Waiting for $DATA_PATH volume to be ready ..."
    attempts=0
    until mkdir -p "$DATA_PATH/files/blobs" && touch "$DATA_PATH/.ready" 2>/dev/null; do
        attempts=$((attempts + 1))
        if [ $attempts -ge 15 ]; then
            echo "ERROR: $DATA_PATH volume not writable after 30s — check Railway volume config"
            exit 1
        fi
        sleep 2
    done
    rm -f "$DATA_PATH/.ready"
    echo "$DATA_PATH volume is ready"

    if [ ! -f /app/dashboard/.next/standalone/server.js ]; then
        echo "Next.js standalone build not found at /app/dashboard/.next/standalone/server.js"
        exit 1
    fi

    if [ ! -f /app/server/mesahub-server ]; then
        echo "Go server binary not found at /app/server/mesahub-server"
        exit 1
    fi

    echo "Starting Go server and Next.js via supervisord ..."

    # Validate required Go server env vars before launching — fail fast with a clear message
    missing=""
    for var in ADMIN_TOKEN SESSION_SECRET FILE_TOKEN_SIGNING_SECRET; do
        eval "val=\${$var:-}"
        if [ -z "$val" ]; then
            missing="$missing $var"
        fi
    done
    if [ -n "$missing" ]; then
        echo "ERROR: The following required environment variables are not set:$missing"
        echo "Set them in Railway > Variables and redeploy."
        exit 1
    fi

    PORT=$GO_PORT supervisord -c /etc/supervisor/conf.d/mesahub.conf &
    SUPERVISOR_PID=$!

    echo "Waiting for Go server on :$GO_PORT ..."
    max_attempts=30
    attempt=0
    until curl -sf http://localhost:$GO_PORT/api/health > /dev/null 2>&1; do
        attempt=$((attempt + 1))
        if [ $attempt -eq $max_attempts ]; then
            echo "Go server failed to start on port $GO_PORT"
            kill $SUPERVISOR_PID 2>/dev/null || true
            exit 1
        fi
        sleep 2
    done
    echo "Go server ready"

    echo "Waiting for Next.js server on :$NEXTJS_PORT ..."
    max_attempts=60
    attempt=0
    until curl -sf http://localhost:$NEXTJS_PORT/api/health > /dev/null 2>&1; do
        attempt=$((attempt + 1))
        if [ $attempt -eq $max_attempts ]; then
            echo "Next.js server failed to start on port $NEXTJS_PORT"
            kill $SUPERVISOR_PID 2>/dev/null || true
            exit 1
        fi
        sleep 2
    done
    echo "Next.js server ready"

    CONTROL_ENABLED_VAL="$(echo "${ENABLE_CONTROL_DB:-false}" | tr '[:upper:]' '[:lower:]')"

    # ── Caddyfile header ──────────────────────────────────────────────────────
    cat > /tmp/Caddyfile <<EOF
{
    auto_https off
    admin off
}

:${PORT} {
    @dslash path_regexp dslash ^//(.*)$
    rewrite @dslash /{http.regexp.dslash.1}
EOF

    # ── Named-host routing (only when split domains are configured) ───────────
    if [ "$CONTROL_ENABLED_VAL" = "true" ] && [ -n "$API_FULL_HOSTNAME" ] && [ -n "$ADMIN_HOST_LIST" ]; then
        cat >> /tmp/Caddyfile <<EOF

    # --- ${API_FULL_HOSTNAME} -> Go API server only ---
    # SDK clients send /v1/* directly; admin API calls send /api/*.
    # Never proxies to Next.js — browsers get a plain 200 at the root.
    @api_host host ${API_FULL_HOSTNAME}
    handle @api_host {
        handle / {
            respond "OK" 200
        }
        handle {
            reverse_proxy localhost:${GO_PORT}
        }
    }

    # --- ${ADMIN_HOST_LIST} -> Next.js admin UI ---
    @admin_host host ${ADMIN_HOST_LIST}
    handle @admin_host {
        reverse_proxy localhost:${NEXTJS_PORT}
    }
EOF
    fi

    # ── Path-based fallback routing (standalone + Railway preview URLs) ───────
    cat >> /tmp/Caddyfile <<EOF

    # SDK data-plane routes → Go directly (bypasses Next.js auth middleware).
    handle /v1/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/exec/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/query/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/rest/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/files/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/buckets/* {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/health {
        reverse_proxy localhost:${GO_PORT}
    }
    handle /api/version {
        reverse_proxy localhost:${GO_PORT}
    }

    # Everything else (dashboard UI, /api/db/*, auth routes) → Next.js
    handle {
        reverse_proxy localhost:${NEXTJS_PORT}
    }
}
EOF

    echo "Formatting Caddyfile ..."
    caddy fmt --overwrite /tmp/Caddyfile

    echo "Validating Caddyfile ..."
    caddy validate --config /tmp/Caddyfile || { echo "Caddyfile validation failed"; exit 1; }

    echo "Starting Caddy on port $PORT ..."
    exec caddy run --config /tmp/Caddyfile
fi
