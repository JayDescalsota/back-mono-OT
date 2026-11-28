#!/bin/bash
set -e

# Monolith ops: single Go binary + single Postgres DB + single Redis.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATION_DIR="$SCRIPT_DIR/migrations"

if [ -f "$SCRIPT_DIR/.env" ]; then
  export $(grep -v '^#' "$SCRIPT_DIR/.env" | xargs)
fi

usage() {
  cat <<EOF
Usage: $0 {up|down|build|setup|migrate|gqlgen} [options]

Commands:
  up          Start services via compose (Docker or Podman)
  down        Stop services via compose (Docker or Podman)
  build       Regenerate GraphQL, build Go binary, restart app container
  setup       Reset containers, run migrations, and start everything
  migrate     Run migrations only
  gqlgen      Regenerate the unified GraphQL schema

Migration forms: migrate | migrate VERSION up|down
EOF
  exit 1
}

CMD="$1"
if [ -z "$CMD" ]; then
  usage
fi
shift || true

MIGRATION_VERSION=""
MIGRATION_ACTION="latest"
for arg in "$@"; do
  if [[ "$arg" != -* ]] && [ -z "$MIGRATION_VERSION" ]; then
    MIGRATION_VERSION="$arg"
  elif [[ "$arg" != -* ]] && [ -n "$MIGRATION_VERSION" ] && [ -z "$MIGRATION_ACTION" ] || [[ "$arg" == "up" || "$arg" == "down" ]]; then
    MIGRATION_ACTION="$arg"
  fi
done

case "$CMD" in
  up|down|build|setup|migrate|gqlgen) ;;
  *) echo "Unknown command: $CMD" >&2; usage ;;
esac

DOCKER_COMPOSE=""
ensure_container_running() {
  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    if docker compose version >/dev/null 2>&1; then
      DOCKER_COMPOSE="docker compose"
    elif command -v docker-compose >/dev/null 2>&1; then
      DOCKER_COMPOSE="docker-compose"
    else
      DOCKER_COMPOSE="docker compose"
    fi
    echo "Docker is running ($DOCKER_COMPOSE)."
    return 0
  fi
  if command -v podman >/dev/null 2>&1 && podman info >/dev/null 2>&1; then
    if podman compose version >/dev/null 2>&1; then
      DOCKER_COMPOSE="podman compose"
    elif command -v podman-compose >/dev/null 2>&1; then
      DOCKER_COMPOSE="podman-compose"
    else
      DOCKER_COMPOSE="podman compose"
    fi
    echo "Podman is running ($DOCKER_COMPOSE)."
    return 0
  fi
  echo "No active container runtime found. Start Docker Desktop or 'podman machine start'." >&2
  exit 1
}

ensure_go() {
  if ! command -v go >/dev/null 2>&1; then
    echo "Go is not installed, but is required for '$CMD'." >&2
    exit 1
  fi
}

get_db_name() {
  local url="$1"
  local url_no_query="${url%%\?*}"
  echo "${url_no_query##*/}"
}

wait_for_db() {
  echo "Waiting for database to be ready..."
  until $DOCKER_COMPOSE exec -T db pg_isready -U postgres >/dev/null 2>&1 && \
        $DOCKER_COMPOSE exec -T db psql -U postgres -d postgres -c "SELECT 1;" >/dev/null 2>&1; do
    sleep 1
  done
  echo "Database is ready."
}

ensure_databases() {
  local db_name
  db_name="$(get_db_name "$DB_URL")"
  if [ -n "$db_name" ] && [ "$db_name" != "postgres" ]; then
    if ! $DOCKER_COMPOSE exec -T db psql -U postgres -d postgres -Atc "SELECT 1 FROM pg_database WHERE datname = '$db_name';" | grep -q 1; then
      $DOCKER_COMPOSE exec -T db psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$db_name\";" >/dev/null
    fi
  fi
}

ensure_migration_table() {
  $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -c '
    CREATE TABLE IF NOT EXISTS schema_migrations (
      version TEXT PRIMARY KEY,
      dirty BOOLEAN NOT NULL DEFAULT false,
      applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );
  ' >/dev/null
}

apply_migration() {
  local migration="$1"
  local filename version state down_migration
  filename="$(basename "$migration")"
  version="${filename%.up.sql}"
  state="$($DOCKER_COMPOSE exec -T db psql "$DB_URL" -Atc "SELECT CASE WHEN dirty THEN 'dirty' ELSE 'clean' END FROM schema_migrations WHERE version = '$version';" | tr -d '\r')"
  if [ "$state" = "clean" ]; then
    echo "  Skipping $filename (already applied)"
    return 0
  fi
  if [ "$state" = "dirty" ]; then
    echo "  $filename is marked dirty; auto-healing..."
    down_migration="${migration%.up.sql}.down.sql"
    if [ -f "$down_migration" ]; then
      echo "  Rolling back $filename using $(basename "$down_migration")..."
      $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -f - < "$down_migration" >/dev/null 2>&1 || true
    fi
    $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -c "DELETE FROM schema_migrations WHERE version = '$version';" >/dev/null
    echo "  Dirty state cleared for $version. Retrying application..."
  fi
  echo "  Applying $filename..."
  $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -c "INSERT INTO schema_migrations (version, dirty) VALUES ('$version', true) ON CONFLICT (version) DO UPDATE SET dirty = true;" >/dev/null
  if ! $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -f - < "$migration" >/dev/null; then
    echo "  ERROR: $filename failed and remains marked dirty" >&2
    return 1
  fi
  $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -c "UPDATE schema_migrations SET dirty = false, applied_at = NOW() WHERE version = '$version';" >/dev/null
}

rollback_migration() {
  local migration="$1"
  local filename version state down_migration
  filename="$(basename "$migration")"
  version="${filename%.up.sql}"
  state="$($DOCKER_COMPOSE exec -T db psql "$DB_URL" -Atc "SELECT CASE WHEN dirty THEN 'dirty' ELSE 'clean' END FROM schema_migrations WHERE version = '$version';" | tr -d '\r')"
  if [ "$state" != "clean" ]; then
    echo "  ERROR: $filename is not applied cleanly" >&2
    return 1
  fi
  down_migration="${migration%.sql}.down.sql"
  if [ ! -f "$down_migration" ]; then
    echo "  ERROR: no rollback file for $filename ($down_migration)" >&2
    return 1
  fi
  echo "  Rolling back $filename..."
  $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -c "UPDATE schema_migrations SET dirty = true WHERE version = '$version';" >/dev/null
  if ! $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -f - < "$down_migration" >/dev/null; then
    echo "  ERROR: rollback for $filename failed and remains marked dirty" >&2
    return 1
  fi
  $DOCKER_COMPOSE exec -T db psql "$DB_URL" -v ON_ERROR_STOP=1 -c "DELETE FROM schema_migrations WHERE version = '$version';" >/dev/null
}

run_migrations() {
  ensure_migration_table
  if [ -z "$MIGRATION_VERSION" ] || [ "$MIGRATION_ACTION" = "latest" ]; then
    for migration in "$MIGRATION_DIR"/*.up.sql; do
      [ -f "$migration" ] || continue
      apply_migration "$migration" || return 1
    done
  else
    local migration
    migration="$(ls "$MIGRATION_DIR/${MIGRATION_VERSION}"*.up.sql 2>/dev/null | head -n 1)"
    if [ -z "$migration" ]; then
      echo "  ERROR: migration version $MIGRATION_VERSION not found" >&2
      return 1
    fi
    if [ "$MIGRATION_ACTION" = "up" ]; then
      apply_migration "$migration" || return 1
    else
      rollback_migration "$migration" || return 1
    fi
  fi
}

generate_gqlgen() {
  echo "--- Generating gqlgen (unified schema) ---"
  (cd "$SCRIPT_DIR" && go run github.com/99designs/gqlgen generate)
}

case "$CMD" in
  up|down|build|setup|migrate)
    ensure_container_running
    ;;
esac

case "$CMD" in
  build|gqlgen)
    ensure_go
    ;;
esac

case "$CMD" in
  up)
    $DOCKER_COMPOSE up -d
    ;;
  down)
    $DOCKER_COMPOSE down
    ;;
  build)
    generate_gqlgen
    echo "Building monolith..."
    (cd "$SCRIPT_DIR" && go build -o "bin/server" "./cmd/server")
    $DOCKER_COMPOSE up -d --build app
    ;;
  setup)
    if [ -d "$SCRIPT_DIR/.git" ] && [ -d "$SCRIPT_DIR/.githooks" ]; then
      git config core.hooksPath .githooks >/dev/null 2>&1 || true
    fi
    echo "Stopping and removing all services..."
    $DOCKER_COMPOSE down -v
    echo "Starting database and redis..."
    $DOCKER_COMPOSE up -d db redis
    wait_for_db
    ensure_databases
    run_migrations
    echo "Building and starting all services..."
    $DOCKER_COMPOSE up -d --build
    ;;
  migrate)
    echo "Starting database..."
    $DOCKER_COMPOSE up -d db
    wait_for_db
    ensure_databases
    run_migrations
    ;;
  gqlgen)
    generate_gqlgen
    ;;
esac
