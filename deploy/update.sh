#!/usr/bin/env bash
# update.sh — runs ON the production server, under sudo. It is shipped to
# /tmp/update.sh alongside the binary on every deploy; the server keeps no
# git checkout. Given a freshly-built binary at /tmp/lotus.new it:
#   1. verifies sha256 (if the sidecar is present)
#   2. runs pending migrations with the OLD binary still serving
#   3. swaps the binary in atomically, keeping .previous for rollback
#   4. restarts the unit and health-checks /healthz, rolling back on failure
#
# Usage: sudo bash /tmp/update.sh /tmp/lotus.new
set -euo pipefail

NEW_BIN="${1:-/tmp/lotus.new}"
APP_DIR="/opt/lotuscheck"
SERVICE="lotuscheck-web"
APP_USER="lotuscheck"
ENV_FILE="/etc/lotuscheck/lotuscheck.env"
TARGET_BIN="${APP_DIR}/bin/lotus"

log() { echo "[$(date +'%F %T')] $*"; }

# Health-check the port the app actually binds, read from ADDR in the env
# file so this can never drift from the configured port. ":8085" and
# "0.0.0.0:8085" are normalized to a loopback address for curl.
addr="$(grep -E '^ADDR=' "${ENV_FILE}" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d '[:space:]')"
addr="${addr:-127.0.0.1:8085}"
case "${addr}" in
  :*)        addr="127.0.0.1${addr}" ;;
  0.0.0.0:*) addr="127.0.0.1:${addr#0.0.0.0:}" ;;
esac
HEALTH_URL="http://${addr}/healthz"

[[ -f "${NEW_BIN}" ]] || { log "❌ New binary not found: ${NEW_BIN}"; exit 1; }
[[ -f "${ENV_FILE}" ]] || { log "❌ Missing ${ENV_FILE} — run deploy/bootstrap.sh first"; exit 1; }

if [[ -f "${NEW_BIN}.sha256" ]]; then
  log "🔐 Verifying sha256..."
  expected="$(awk '{print $1}' "${NEW_BIN}.sha256")"
  actual="$(sha256sum "${NEW_BIN}" | awk '{print $1}')"
  if [[ "${expected}" != "${actual}" ]]; then
    log "❌ sha256 mismatch: expected=${expected} actual=${actual}"
    exit 1
  fi
  log "  ok"
fi

log "📂 Ensure target dirs..."
install -d -o "${APP_USER}" -g "${APP_USER}" "${APP_DIR}" "${APP_DIR}/bin"

# Migrations run BEFORE the swap, from the new binary, while the old one is
# still serving. Lotus never migrates at boot (internal/db doc comment):
# with more than one replica that races, and the app role has no DDL rights
# anyway. Running them here means a migration failure aborts the deploy with
# the old version still up, rather than leaving a new binary against an old
# schema.
log "🗃️  Running migrations..."
install -o "${APP_USER}" -g "${APP_USER}" -m 0755 "${NEW_BIN}" "${APP_DIR}/bin/lotus.staged"
# The env file is NOT sourced. systemd's EnvironmentFile format takes values
# raw, so SMTP_FROM=Lotus <no-reply@javan.co.id> is perfectly valid there —
# and sourcing that line in a shell would parse `<` as a redirect. Read the
# handful of keys `lotus migrate` needs (config.Validate runs for every
# subcommand, so the mode keys are required even here) one at a time.
envval() { grep -E "^$1=" "${ENV_FILE}" | tail -1 | cut -d= -f2-; }
if ! runuser -u "${APP_USER}" -- env \
     DATABASE_URL="$(envval DATABASE_URL)" \
     MIGRATE_DATABASE_URL="$(envval MIGRATE_DATABASE_URL)" \
     LOTUS_MODE="$(envval LOTUS_MODE)" \
     LOTUS_URL="$(envval LOTUS_URL)" \
     LOTUS_BASE_DOMAIN="$(envval LOTUS_BASE_DOMAIN)" \
     LOTUS_DEV=0 \
     "${APP_DIR}/bin/lotus.staged" migrate up; then
  log "❌ Migration failed — the running version was left untouched."
  rm -f "${APP_DIR}/bin/lotus.staged"
  exit 1
fi

log "🔁 Atomic swap binary..."
if [[ -f "${TARGET_BIN}" ]]; then
  cp -f "${TARGET_BIN}" "${TARGET_BIN}.previous"
fi
# install(1) writes via a temporary and renames, so no request ever sees a
# half-written file.
install -o "${APP_USER}" -g "${APP_USER}" -m 0755 "${NEW_BIN}" "${TARGET_BIN}"
rm -f "${NEW_BIN}" "${NEW_BIN}.sha256" "${APP_DIR}/bin/lotus.staged"

log "♻️  Restarting ${SERVICE}..."
systemctl restart "${SERVICE}"

log "🩺 Health check ${HEALTH_URL}..."
ok=0
for i in $(seq 1 10); do
  if curl -fsS --max-time 3 "${HEALTH_URL}" >/dev/null 2>&1; then ok=1; break; fi
  sleep 3
done

if [[ "${ok}" -ne 1 ]]; then
  log "❌ Health check FAILED. Recent logs:"
  journalctl -u "${SERVICE}" -n 80 --no-pager || true
  if [[ -f "${TARGET_BIN}.previous" ]]; then
    log "⏪ Rolling back to the previous binary..."
    install -o "${APP_USER}" -g "${APP_USER}" -m 0755 "${TARGET_BIN}.previous" "${TARGET_BIN}"
    systemctl restart "${SERVICE}"
    log "   Rolled back. NOTE: migrations already applied are NOT rolled back."
  fi
  exit 1
fi

log "✅ Deployment OK."
