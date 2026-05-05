# MesaHub Core — runtime commands
# Install: brew install just

DEFAULT_PORT := env_var_or_default('MESAHUB_PORT', '8080')
PORTLESS_ALIAS := "mesahub"
PORTLESS_PROXY_PORT := "1355"
DEV_SERVICE := "app-dev"
PROD_SERVICE := "app-prod"

default:
    @just --list

# Validate local prerequisites and runtime contract.
doctor:
    #!/usr/bin/env bash
    set -euo pipefail

    command -v docker >/dev/null 2>&1 || { echo "❌ docker is required"; exit 1; }
    docker info >/dev/null 2>&1 || { echo "❌ Docker daemon is not running"; exit 1; }

    command -v just >/dev/null 2>&1 || { echo "❌ just is required"; exit 1; }
    command -v node >/dev/null 2>&1 || { echo "❌ node is required"; exit 1; }
    command -v npm >/dev/null 2>&1 || { echo "❌ npm is required"; exit 1; }

    [ -f .env ] || { echo "❌ .env missing. Run: cp .env.example .env"; exit 1; }

    npx portless --help >/dev/null 2>&1 || { echo "❌ Portless not available via npx"; exit 1; }

    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose --profile dev config >/dev/null
      docker-compose --profile prod config >/dev/null
    else
      docker compose --profile dev config >/dev/null
      docker compose --profile prod config >/dev/null
    fi

    echo "✅ doctor: environment is ready"

# Validate compose profile contract (CI-friendly).
contract:
    #!/usr/bin/env bash
    set -euo pipefail

    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose --profile dev config >/dev/null
      docker-compose --profile prod config >/dev/null
    else
      docker compose --profile dev config >/dev/null
      docker compose --profile prod config >/dev/null
    fi

    echo "✅ contract: compose profiles are valid"

# Dev: live mode, random Docker host port, host Portless alias.
# Ctrl+C stops the container.
dev:
    #!/usr/bin/env bash
    set -euo pipefail

    [ -f .env ] || { echo "❌ .env missing. Run: cp .env.example .env"; exit 1; }
    if command -v docker-compose >/dev/null 2>&1; then
      COMPOSE="docker-compose"
    else
      COMPOSE="docker compose"
    fi

    npx portless proxy start >/dev/null 2>&1 || true

    cleanup() {
      $COMPOSE --profile dev down --remove-orphans >/dev/null 2>&1 || true
      npx portless alias --remove {{PORTLESS_ALIAS}} >/dev/null 2>&1 || true
    }
    trap cleanup EXIT INT TERM

    # Start detached first so we can deterministically discover the fresh mapped port.
    $COMPOSE --profile dev up -d --build --force-recreate {{DEV_SERVICE}}

    HOST_PORT=""
    tries=0
    max_tries=90
    while [ $tries -lt $max_tries ]; do
      HOST_PORT=$($COMPOSE --profile dev port {{DEV_SERVICE}} 80 2>/dev/null | awk -F: '{print $NF}')
      if [ -n "$HOST_PORT" ]; then
        break
      fi
      tries=$((tries + 1))
      sleep 1
    done

    [ -n "$HOST_PORT" ] || {
      echo "❌ Could not detect dev mapped port within timeout"
      $COMPOSE --profile dev logs --tail=100 {{DEV_SERVICE}} || true
      exit 1
    }

    npx portless alias {{PORTLESS_ALIAS}} "$HOST_PORT" >/dev/null 2>&1 || true
    echo "Dev URL: http://{{PORTLESS_ALIAS}}.localhost:{{PORTLESS_PROXY_PORT}}"

    health_tries=0
    health_max=120
    while [ $health_tries -lt $health_max ]; do
      if curl -sf "http://localhost:$HOST_PORT/api/health" >/dev/null 2>&1; then
        echo "✅ Dev service is healthy"
        break
      fi
      health_tries=$((health_tries + 1))
      sleep 1
    done

    if [ $health_tries -ge $health_max ]; then
      echo "⚠️ Dev service did not become healthy within timeout"
    fi

    # Foreground log stream; Ctrl+C triggers trap and stops dev container.
    $COMPOSE --profile dev logs -f {{DEV_SERVICE}}

# Dev daemon: detached mode with Portless alias and health check.
# Service keeps running until `just down` is called.
dev-daemon:
    #!/usr/bin/env bash
    set -euo pipefail

    [ -f .env ] || { echo "❌ .env missing. Run: cp .env.example .env"; exit 1; }
    if command -v docker-compose >/dev/null 2>&1; then
      COMPOSE="docker-compose"
    else
      COMPOSE="docker compose"
    fi

    npx portless proxy start >/dev/null 2>&1 || true

    $COMPOSE --profile dev up -d --build --force-recreate {{DEV_SERVICE}}

    HOST_PORT=""
    tries=0
    max_tries=90
    while [ $tries -lt $max_tries ]; do
      HOST_PORT=$($COMPOSE --profile dev port {{DEV_SERVICE}} 80 2>/dev/null | awk -F: '{print $NF}')
      if [ -n "$HOST_PORT" ]; then
        break
      fi
      tries=$((tries + 1))
      sleep 1
    done

    [ -n "$HOST_PORT" ] || {
      echo "❌ Could not detect dev mapped port within timeout"
      $COMPOSE --profile dev logs --tail=100 {{DEV_SERVICE}} || true
      exit 1
    }

    npx portless alias --remove {{PORTLESS_ALIAS}} >/dev/null 2>&1 || true
    npx portless alias {{PORTLESS_ALIAS}} "$HOST_PORT" >/dev/null 2>&1 || true
    echo "Dev URL: http://{{PORTLESS_ALIAS}}.localhost:{{PORTLESS_PROXY_PORT}}"

    health_tries=0
    health_max=120
    while [ $health_tries -lt $health_max ]; do
      if curl -sf "http://localhost:$HOST_PORT/api/health" >/dev/null 2>&1; then
        echo "✅ Dev daemon service is healthy"
        break
      fi
      health_tries=$((health_tries + 1))
      sleep 1
    done

    if [ $health_tries -ge $health_max ]; then
      echo "⚠️ Dev daemon service did not become healthy within timeout"
    fi

    echo "Daemon mode active. Use 'just logs {{DEV_SERVICE}}' to follow logs and 'just down' to stop."

# Prod: fixed host port for local production-like runs.
prod port=DEFAULT_PORT:
    #!/usr/bin/env bash
    set -euo pipefail

    [ -f .env ] || { echo "❌ .env missing. Run: cp .env.example .env"; exit 1; }
    echo "Starting production mode on port {{port}}"

    if command -v docker-compose >/dev/null 2>&1; then
      PORT={{port}} docker-compose --profile prod up {{PROD_SERVICE}}
    else
      PORT={{port}} docker compose --profile prod up {{PROD_SERVICE}}
    fi

down:
    #!/usr/bin/env bash
    set -euo pipefail

    echo "Stopping containers..."
    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose --profile dev --profile prod down
    else
      docker compose --profile dev --profile prod down
    fi
    npx portless alias --remove {{PORTLESS_ALIAS}} >/dev/null 2>&1 || true

# Remove stale containers from earlier naming/layout.
dev-clean:
    #!/usr/bin/env bash
    set -euo pipefail

    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose --profile dev --profile prod down -v --remove-orphans || true
    else
      docker compose --profile dev --profile prod down -v --remove-orphans || true
    fi
    docker rm -f mesahub mesahub-dev mesahub-prod app app-dev app-prod >/dev/null 2>&1 || true
    npx portless alias --remove {{PORTLESS_ALIAS}} >/dev/null 2>&1 || true
    echo "✅ development artifacts cleaned"

logs service=DEV_SERVICE:
    #!/usr/bin/env bash
    set -euo pipefail

    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose logs -f {{service}}
    else
      docker compose logs -f {{service}}
    fi

status:
    #!/usr/bin/env bash
    set -euo pipefail

    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose ps
    else
      docker compose ps
    fi

show-ports:
    #!/usr/bin/env bash
    set -euo pipefail

    echo "Dev mapped port:"
    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose --profile dev port {{DEV_SERVICE}} 80 || true
    else
      docker compose --profile dev port {{DEV_SERVICE}} 80 || true
    fi

    echo "Prod mapped port:"
    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose --profile prod port {{PROD_SERVICE}} 80 || true
    else
      docker compose --profile prod port {{PROD_SERVICE}} 80 || true
    fi

# Quick local smoke checks against running service.
smoke:
    #!/usr/bin/env bash
    set -euo pipefail

    [ -f .env ] || { echo "❌ .env missing. Run: cp .env.example .env"; exit 1; }
    ADMIN_TOKEN=$(grep -E '^ADMIN_TOKEN=' .env | head -n1 | cut -d= -f2-)
    [ -n "$ADMIN_TOKEN" ] || { echo "❌ ADMIN_TOKEN missing in .env"; exit 1; }

    if command -v docker-compose >/dev/null 2>&1; then
      DEV_PORT=$(docker-compose --profile dev port {{DEV_SERVICE}} 80 2>/dev/null | awk -F: '{print $NF}' || true)
      PROD_PORT=$(docker-compose --profile prod port {{PROD_SERVICE}} 80 2>/dev/null | awk -F: '{print $NF}' || true)
    else
      DEV_PORT=$(docker compose --profile dev port {{DEV_SERVICE}} 80 2>/dev/null | awk -F: '{print $NF}' || true)
      PROD_PORT=$(docker compose --profile prod port {{PROD_SERVICE}} 80 2>/dev/null | awk -F: '{print $NF}' || true)
    fi

    if [ -n "$DEV_PORT" ]; then
      BASE_URL="http://localhost:$DEV_PORT"
    elif [ -n "$PROD_PORT" ]; then
      BASE_URL="http://localhost:$PROD_PORT"
    else
      echo "❌ No running dev/prod service found. Start with just dev or just prod"
      exit 1
    fi

    echo "Running smoke checks against $BASE_URL"
    code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/api/health")
    [ "$code" = "200" ] || { echo "❌ /api/health returned $code"; exit 1; }

    code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE_URL/api/auth/login" \
      -H 'Content-Type: application/json' \
      -d "{\"token\":\"$ADMIN_TOKEN\"}")
    [ "$code" = "200" ] || { echo "❌ /api/auth/login returned $code"; exit 1; }

    echo "✅ smoke checks passed"

build-dev:
    #!/usr/bin/env bash
    set -euo pipefail

    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose --profile dev build {{DEV_SERVICE}}
    else
      docker compose --profile dev build {{DEV_SERVICE}}
    fi

build-prod:
    #!/usr/bin/env bash
    set -euo pipefail

    if command -v docker-compose >/dev/null 2>&1; then
      docker-compose --profile prod build {{PROD_SERVICE}}
    else
      docker compose --profile prod build {{PROD_SERVICE}}
    fi

check:
    npx tsc --noEmit --skipLibCheck
    npx next build
