#!/usr/bin/env bash
# deploy/bootstrap.sh — one-time production provisioning. Run FROM YOUR
# LAPTOP, not on the server.
#
# Creates the app user, directories, Postgres roles and database, and
# installs the systemd unit and nginx site over SSH. The server keeps no git
# checkout: only the binary and its config live there. After this, deploys
# are `deploy/deploy.sh` or a push to main (GitLab CI).
#
# Everything is namespaced `lotuscheck` rather than `lotus`, because an
# unrelated application (the AlurKerja on-premise toolkit serving
# lotus.alurkerja.com) already owns the `lotus` user, /opt/lotus,
# /etc/lotus, lotus-web.service and port 8080 on this host.
#
# Usage:
#   PROD_SERVER_USER=ubuntu PROD_SERVER_HOST=collab DOMAIN=lotus.javan.id \
#     ./deploy/bootstrap.sh
#
# Idempotent: safe to re-run. Existing roles keep their passwords.
set -euo pipefail

cd "$(dirname "$0")"

SERVER_USER="${PROD_SERVER_USER:-ubuntu}"
SERVER_HOST="${PROD_SERVER_HOST:-}"
DOMAIN="${DOMAIN:-lotus.javan.id}"
APP_PORT="${APP_PORT:-8085}"

if [[ -z "${SERVER_HOST}" ]]; then
  echo "Set PROD_SERVER_HOST (an ssh alias like 'collab' is fine)." >&2
  exit 1
fi

# An ssh alias carries its own User, so only build user@host when the alias
# does not already resolve one.
REMOTE="${SERVER_HOST}"
if [[ "${SERVER_HOST}" == *.* || "${SERVER_HOST}" =~ ^[0-9] ]]; then
  REMOTE="${SERVER_USER}@${SERVER_HOST}"
fi

log() { echo "==> $*"; }

log "Testing SSH to ${REMOTE}..."
ssh -o ConnectTimeout=10 "${REMOTE}" "echo ok" >/dev/null

log "Installing prerequisites..."
ssh "${REMOTE}" "sudo apt-get update -qq && sudo apt-get install -y --no-install-recommends \
  nginx certbot python3-certbot-nginx postgresql postgresql-client curl ca-certificates"

log "Creating app user + directories..."
ssh "${REMOTE}" "sudo useradd --system --create-home --shell /usr/sbin/nologin lotuscheck 2>/dev/null || true; \
  sudo install -d -o lotuscheck -g lotuscheck /opt/lotuscheck /opt/lotuscheck/bin; \
  sudo install -d -m 0750 -o root -g lotuscheck /etc/lotuscheck"

# ---------------------------------------------------------------------------
# Postgres. Two login roles, mirroring the DDL/DML split the schema is built
# around (see internal/db/migrations/00003_tenant_plane.sql):
#
#   lotuscheck_migrate  owns the schema, runs `lotus migrate`
#   lotuscheck_app      the app's runtime role — DML only, and crucially NOT
#                       a superuser, because FORCE ROW LEVEL SECURITY is
#                       bypassed unconditionally for superusers. Connecting
#                       as a superuser would leave every tenant-isolation
#                       policy silently inert.
#
# The NOLOGIN lotus_migrate / lotus_app group roles are normally created by
# migration 00003. They are created here first so membership can be granted
# in this one pass; 00003's DO block swallows duplicate_object, so
# pre-creating them is harmless.
# ---------------------------------------------------------------------------
log "Creating Postgres roles + database (idempotent)..."
APP_PASSWORD="${APP_PASSWORD:-$(openssl rand -hex 24)}"
MIGRATE_PASSWORD="${MIGRATE_PASSWORD:-$(openssl rand -hex 24)}"

psql_c() { ssh "${REMOTE}" "sudo -u postgres psql -v ON_ERROR_STOP=1 -tAc \"$1\""; }

for grp in lotus_migrate lotus_app; do
  exists="$(psql_c "SELECT 1 FROM pg_roles WHERE rolname='${grp}'")"
  [[ "${exists}" == "1" ]] || psql_c "CREATE ROLE ${grp} NOLOGIN" >/dev/null
done

created_app=0
created_migrate=0
for pair in "lotuscheck_app:${APP_PASSWORD}" "lotuscheck_migrate:${MIGRATE_PASSWORD}"; do
  role="${pair%%:*}"; pass="${pair#*:}"
  exists="$(psql_c "SELECT 1 FROM pg_roles WHERE rolname='${role}'")"
  if [[ "${exists}" == "1" ]]; then
    log "  role '${role}' already exists — password left as-is"
  else
    psql_c "CREATE ROLE ${role} LOGIN PASSWORD '${pass}'" >/dev/null
    [[ "${role}" == "lotuscheck_app" ]] && created_app=1 || created_migrate=1
  fi
done

psql_c "GRANT lotus_app TO lotuscheck_app" >/dev/null
psql_c "GRANT lotus_migrate TO lotuscheck_migrate" >/dev/null

db_exists="$(psql_c "SELECT 1 FROM pg_database WHERE datname='lotuscheck'")"
if [[ "${db_exists}" != "1" ]]; then
  ssh "${REMOTE}" "sudo -u postgres createdb -O lotuscheck_migrate lotuscheck"
  log "  database 'lotuscheck' created"
fi
# CREATE EXTENSION needs superuser, so it cannot live in migration 00001 for
# a non-superuser migrate role. Doing it here keeps that migration a no-op
# instead of a hard failure on first deploy.
ssh "${REMOTE}" "sudo -u postgres psql -v ON_ERROR_STOP=1 -d lotuscheck -c 'CREATE EXTENSION IF NOT EXISTS pgcrypto' -c 'CREATE EXTENSION IF NOT EXISTS \\\"uuid-ossp\\\"'" >/dev/null 2>&1 || true
psql_c "GRANT CONNECT ON DATABASE lotuscheck TO lotuscheck_app" >/dev/null

log "Uploading systemd unit + nginx site (domain: ${DOMAIN}, port: ${APP_PORT})..."
tmp_nginx="$(mktemp)"
sed -e "s/lotus\.javan\.id/${DOMAIN}/g" -e "s/127\.0\.0\.1:8085/127.0.0.1:${APP_PORT}/g" \
  nginx-lotuscheck.conf > "${tmp_nginx}"
scp -q lotuscheck-web.service "${REMOTE}:/tmp/lotuscheck-web.service"
scp -q "${tmp_nginx}" "${REMOTE}:/tmp/nginx-lotuscheck.conf"
rm -f "${tmp_nginx}"

log "Installing systemd unit + nginx site..."
ssh "${REMOTE}" bash <<'ENDSSH'
  set -e
  sudo install -m 0644 /tmp/lotuscheck-web.service /etc/systemd/system/lotuscheck-web.service
  rm -f /tmp/lotuscheck-web.service
  sudo systemctl daemon-reload
  sudo systemctl enable lotuscheck-web

  sudo install -m 0644 /tmp/nginx-lotuscheck.conf /etc/nginx/sites-available/lotuscheck
  rm -f /tmp/nginx-lotuscheck.conf
  sudo ln -sf /etc/nginx/sites-available/lotuscheck /etc/nginx/sites-enabled/lotuscheck
  sudo nginx -t
  sudo systemctl reload nginx
ENDSSH

cat <<EOF

✅ Base provisioning done.

$( [[ "${created_app}" == "1" ]] && cat <<PW
🔑 Save these NOW — they are not stored anywhere else:

   DATABASE_URL
     postgres://lotuscheck_app:${APP_PASSWORD}@127.0.0.1:5432/lotuscheck?sslmode=disable
   MIGRATE_DATABASE_URL
     postgres://lotuscheck_migrate:${MIGRATE_PASSWORD}@127.0.0.1:5432/lotuscheck?sslmode=disable
PW
)

Remaining steps:
  1. Set the GitLab CI/CD variables (Settings → CI/CD → Variables), all
     masked except the non-secret ones:
       SSH_PRIVATE_KEY   PROD_SERVER_HOST   PROD_SERVER_USER
       DATABASE_URL      MIGRATE_DATABASE_URL          (from above)
       SESSION_SECRET    openssl rand -hex 32
       LOTUS_KEK         openssl rand -base64 32   ← never rotate by hand
       LOTUS_MODE=single LOTUS_URL=https://${DOMAIN}
  2. Deploy — push to main, or from your laptop:
       PROD_SERVER_HOST=${SERVER_HOST} ./deploy/deploy.sh
  3. TLS. ${DOMAIN} is proxied by Cloudflare; HTTP-01 still works because
     Cloudflare passes /.well-known/acme-challenge/ to the origin:
       ssh ${REMOTE} 'sudo certbot --nginx -d ${DOMAIN} --redirect'
     Until the origin has a real certificate, Cloudflare answers 526.
  4. Create the one organization (single mode holds exactly one):
       ssh ${REMOTE} "sudo -u lotuscheck env \\
         \\\$(sudo grep -E '^(MIGRATE_)?DATABASE_URL=|^LOTUS_(MODE|URL)=' /etc/lotuscheck/lotuscheck.env | xargs) \\
         LOTUS_DEV=0 /opt/lotuscheck/bin/lotus init \\
         --org-name='Javan' --owner-email='you@javan.co.id'"
EOF
