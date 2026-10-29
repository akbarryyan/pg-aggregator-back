# Shared helpers for the database scripts in scripts/. Sourced, not
# executed; callers cd into the directory holding scripts/ (back/ locally,
# /app in the runtime image) with `set -euo pipefail` active.

# load_config sets DB_* (and CASHI_SECRET_KEY) from .config.toml via
# configenv and checks the values psql needs. The dev container has Go and
# the source; the runtime image ships a compiled configenv instead.
load_config() {
  # Declared separately from the assignment so a configenv failure isn't
  # masked by `local`'s own exit status. Two steps on purpose: `eval
  # "$(cmd)"` would swallow the failure too (eval of "" returns 0).
  local config_env
  if command -v go >/dev/null 2>&1; then
    config_env="$(go run ./cmd/configenv)"
  else
    config_env="$(configenv)"
  fi
  eval "$config_env"

  : "${DB_HOST:?DB_HOST is required}"
  : "${DB_PORT:?DB_PORT is required}"
  : "${DB_USER:?DB_USER is required}"
  : "${DB_PASSWORD:?DB_PASSWORD is required}"
  : "${DB_NAME:?DB_NAME is required}"
}

psql_cmd() {
  PGPASSWORD="$DB_PASSWORD" psql \
    -h "$DB_HOST" \
    -p "$DB_PORT" \
    -U "$DB_USER" \
    -d "$DB_NAME" \
    -v ON_ERROR_STOP=1 \
    "$@"
}
