#!/usr/bin/env bash
# db-pull-prod.sh copies the production database into a local one,
# lotus_prod_copy, for testing against real data. Run by `make db-pull-prod`;
# `make dev-prod` then serves the copy.
#
# Production is only ever read: the one command sent to it is pg_dump. Every
# write below goes to the local server, whose address is fixed here rather
# than configurable, so no setting can aim this at production.
#
# The copy holds real names and emails, so it stays on this machine.
# Integration credentials arrive still sealed: without production's LOTUS_KEK,
# which this deliberately does not fetch, the copy cannot open them, so it
# cannot reach GitLab, SonarQube or anything else with them.
set -euo pipefail

cd "$(dirname "$0")/.."

PROD_SSH="${PROD_SSH:-collab}"
PROD_DB="${PROD_DB:-lotuscheck}"
LOCAL_ADMIN="postgres://postgres@localhost:5418/postgres?sslmode=disable"
COPY_DB="lotus_prod_copy"
STAGING_DB="${COPY_DB}_staging"
local_dsn() { echo "postgres://postgres@localhost:5418/$1?sslmode=disable"; }

# Logins and API access are per-environment. Their structure comes across,
# their rows do not: a copied session or token has no business on a laptop.
EXCLUDED_DATA=(sessions auth_tickets oauth_states handoff_handshakes login_tokens api_tokens qa_share_links)

log() { echo "==> $*"; }
admin() { psql "$LOCAL_ADMIN" -v ON_ERROR_STOP=1 -q "$@"; }

for tool in pg_restore psql ssh; do
  command -v "$tool" >/dev/null || { echo "$tool not found" >&2; exit 1; }
done

# Only readable by this user, and gone however the script ends.
umask 077
mkdir -p tmp
DUMP="$(mktemp tmp/prod-dump.XXXXXX)"
trap 'rm -f "$DUMP"' EXIT

excludes=""
for table in "${EXCLUDED_DATA[@]}"; do
  excludes+=" --exclude-table-data=public.$table"
done

log "Dumping $PROD_DB from $PROD_SSH (read-only)..."
# shellcheck disable=SC2029 # $excludes and $PROD_DB expand here on purpose.
ssh "$PROD_SSH" "cd /tmp && sudo -n -u postgres nice -n 10 pg_dump --format=custom$excludes $PROD_DB" >"$DUMP"
pg_restore --list "$DUMP" >/dev/null
log "  $(du -h "$DUMP" | cut -f1)"

# Roles are per server, and the dump names production's. Created as NOLOGIN:
# they exist so ownership and grants restore exactly as they are, not so
# anyone can sign in as them.
for role in lotus_app lotus_migrate lotuscheck_app lotuscheck_migrate; do
  if [[ "$(psql "$LOCAL_ADMIN" -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$role'")" != "1" ]]; then
    admin -c "CREATE ROLE $role NOLOGIN"
  fi
done

# Into a staging database, swapped in only when complete, so a failed pull
# leaves the previous copy usable.
log "Restoring into $STAGING_DB..."
admin -c "DROP DATABASE IF EXISTS $STAGING_DB WITH (FORCE)" -c "CREATE DATABASE $STAGING_DB"
pg_restore --exit-on-error --dbname="$(local_dsn "$STAGING_DB")" "$DUMP"

# The local server has no credentials to sync with, so every due binding
# would fail and fill the dashboard with alerts production does not have.
# LOTUS_WORKER_COUNT cannot prevent it (zero means the default), so nothing
# is left due: no binding comes up for ten years and nothing is queued.
log "Stopping syncs in the copy..."
psql "$(local_dsn "$STAGING_DB")" -v ON_ERROR_STOP=1 -q \
  -c "UPDATE metric_bindings SET next_sync_at = now() + interval '10 years'" \
  -c "UPDATE sync_runs SET status = 'skipped', finished_at = now() WHERE status IN ('queued', 'leased', 'running')"

log "Swapping it in..."
admin -c "DROP DATABASE IF EXISTS $COPY_DB WITH (FORCE)" -c "ALTER DATABASE $STAGING_DB RENAME TO $COPY_DB"

# This checkout may be ahead of production.
log "Applying this checkout's migrations..."
LOTUS_DEV=1 LOTUS_MODE=single LOTUS_URL=http://localhost:8008 \
  DATABASE_URL="$(local_dsn "$COPY_DB")" MIGRATE_DATABASE_URL="$(local_dsn "$COPY_DB")" \
  go run ./cmd/lotus migrate up 2>&1 | tail -1

log "Done. Serve it with: make dev-prod"
