#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT_DIR/deploy/.env"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

export POSTGRES_HOST_PORT="${POSTGRES_HOST_PORT:-15432}"
export REDIS_HOST_PORT="${REDIS_HOST_PORT:-16379}"
export POSTGRES_BIND_HOST="${POSTGRES_BIND_HOST:-127.0.0.1}"
export REDIS_BIND_HOST="${REDIS_BIND_HOST:-127.0.0.1}"

export AUTO_SETUP="${AUTO_SETUP:-true}"
export SERVER_HOST="${SERVER_HOST:-0.0.0.0}"
export SERVER_PORT="${SERVER_PORT:-8080}"
export SERVER_MODE="${SERVER_MODE:-debug}"
export RUN_MODE="${RUN_MODE:-standard}"
export TZ="${TZ:-Asia/Shanghai}"

export DATABASE_HOST="${DEV_DATABASE_HOST:-127.0.0.1}"
export DATABASE_PORT="${DEV_DATABASE_PORT:-$POSTGRES_HOST_PORT}"
export DATABASE_USER="${DATABASE_USER:-${POSTGRES_USER:-sub2api}}"
export DATABASE_PASSWORD="${DATABASE_PASSWORD:-${POSTGRES_PASSWORD:-}}"
export DATABASE_DBNAME="${DATABASE_DBNAME:-${POSTGRES_DB:-sub2api}}"
export DATABASE_SSLMODE="${DATABASE_SSLMODE:-disable}"

export REDIS_HOST="${DEV_REDIS_HOST:-127.0.0.1}"
export REDIS_PORT="${DEV_REDIS_PORT:-$REDIS_HOST_PORT}"
export REDIS_PASSWORD="${REDIS_PASSWORD:-}"
export REDIS_DB="${REDIS_DB:-0}"

if [[ -z "$DATABASE_PASSWORD" ]]; then
  echo "DATABASE_PASSWORD/POSTGRES_PASSWORD is required. Set it in deploy/.env." >&2
  exit 1
fi

compose_args=()
if [[ -f "$ENV_FILE" ]]; then
  compose_args+=(--env-file "$ENV_FILE")
fi

start_db() {
  POSTGRES_HOST_PORT="$POSTGRES_HOST_PORT" \
    REDIS_HOST_PORT="$REDIS_HOST_PORT" \
    POSTGRES_BIND_HOST="$POSTGRES_BIND_HOST" \
    REDIS_BIND_HOST="$REDIS_BIND_HOST" \
    docker compose "${compose_args[@]}" -f "$ROOT_DIR/deploy/docker-compose.dev.yml" up -d postgres redis
}

stop_image_app() {
  docker compose "${compose_args[@]}" -f "$ROOT_DIR/deploy/docker-compose.dev.yml" stop sub2api >/dev/null 2>&1 || true
}

run_app() {
  pnpm --dir "$ROOT_DIR/frontend" run build
  cd "$ROOT_DIR/backend"
  go run -tags embed ./cmd/server
}

case "${1:-run}" in
  db)
    start_db
    ;;
  app)
    run_app
    ;;
  run)
    start_db
    stop_image_app
    run_app
    ;;
  *)
    echo "Usage: tools/dev_local.sh [db|app|run]" >&2
    exit 2
    ;;
esac
