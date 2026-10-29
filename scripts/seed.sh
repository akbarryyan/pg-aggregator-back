#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# shellcheck source=lib/db.sh
. scripts/lib/db.sh
load_config

# Demo data (fake merchants, publicly documented credentials, and an upsert
# that resets the admin password to Admin123!) must never reach production.
# The guard lives here, not only in the Makefile, because the script ships
# in the image and `docker compose run --rm migrate scripts/seed.sh`
# bypasses make.
if [[ "${APP_ENVIRONMENT:-}" == "production" ]]; then
  echo "seed.sh refuses to run with app.environment = \"production\" (demo data); use scripts/seed-admin.sh for the first admin" >&2
  exit 1
fi

for file in seeds/*.sql; do
  echo "Seeding $file"
  psql_cmd -f "$file"
done

echo "Seed completed"
