#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [[ ! -f .env ]]; then
  echo ".env file not found in $ROOT_DIR"
  exit 1
fi

set -a
. ./.env
set +a

: "${DB_HOST:?DB_HOST is required}"
: "${DB_PORT:?DB_PORT is required}"
: "${DB_USER:?DB_USER is required}"
: "${DB_PASSWORD:?DB_PASSWORD is required}"
: "${DB_NAME:?DB_NAME is required}"

psql_cmd() {
  PGPASSWORD="$DB_PASSWORD" psql \
    -h "$DB_HOST" \
    -p "$DB_PORT" \
    -U "$DB_USER" \
    -d "$DB_NAME" \
    -v ON_ERROR_STOP=1 \
    "$@"
}

# Track applied migrations so re-runs skip files that already succeeded.
psql_cmd -c "
CREATE TABLE IF NOT EXISTS schema_migrations (
    filename TEXT PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
"

applied=0
skipped=0

for file in migrations/*.sql; do
  base="$(basename "$file")"

  already_applied="$(
    psql_cmd -tA -c "SELECT 1 FROM schema_migrations WHERE filename = '$base' LIMIT 1;"
  )"

  if [[ "$already_applied" == "1" ]]; then
    echo "Skipping $file (already applied)"
    skipped=$((skipped + 1))
    continue
  fi

  echo "Running $file"
  psql_cmd -f "$file"
  psql_cmd -c "INSERT INTO schema_migrations (filename) VALUES ('$base') ON CONFLICT (filename) DO NOTHING;"
  applied=$((applied + 1))
done

echo "Migrations completed (applied: $applied, skipped: $skipped)"
