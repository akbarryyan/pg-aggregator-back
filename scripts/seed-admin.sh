#!/usr/bin/env bash
# Creates the initial platform admin (seeds/002_admin_user.sql:
# admin@pg-aggregator.local / Admin123!) on a fresh database. That seed is an
# upsert keyed on email, so re-running it would silently reset a changed
# password back to the publicly documented default — this script therefore
# refuses once any admin exists.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# shellcheck source=lib/db.sh
. scripts/lib/db.sh
load_config

admin_count="$(psql_cmd -tA -c "SELECT count(*) FROM admins;")"
if [[ "$admin_count" != "0" ]]; then
  echo "admins table is not empty — seed-admin only runs on a fresh database" >&2
  exit 1
fi

psql_cmd -f seeds/002_admin_user.sql
echo "Admin created: admin@pg-aggregator.local / Admin123!"
echo "Change this password NOW (see DEPLOY.md, section 3) — it is published in the README."
