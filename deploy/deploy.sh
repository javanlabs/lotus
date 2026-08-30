#!/usr/bin/env bash
# deploy/deploy.sh — deploy from your laptop, without GitLab CI. Same shape
# as the CI job: build a static linux/amd64 binary, render the runtime env,
# ship both, run update.sh on the server.
#
# The first deploy has to happen this way, because CI cannot run a pipeline
# for a branch that has not been pushed yet.
#
# Usage:
#   PROD_SERVER_HOST=collab \
#   DATABASE_URL=... MIGRATE_DATABASE_URL=... \
#   SESSION_SECRET=... LOTUS_KEK=... \
#     ./deploy/deploy.sh
set -euo pipefail

cd "$(dirname "$0")/.."

SERVER_USER="${PROD_SERVER_USER:-ubuntu}"
SERVER_HOST="${PROD_SERVER_HOST:-}"
APP_DOMAIN="${APP_DOMAIN:-lotus.javan.id}"
APP_ADDR="${APP_ADDR:-127.0.0.1:8085}"

[[ -n "${SERVER_HOST}" ]] || { echo "Set PROD_SERVER_HOST." >&2; exit 1; }

REMOTE="${SERVER_HOST}"
if [[ "${SERVER_HOST}" == *.* || "${SERVER_HOST}" =~ ^[0-9] ]]; then
  REMOTE="${SERVER_USER}@${SERVER_HOST}"
fi

for required in DATABASE_URL MIGRATE_DATABASE_URL SESSION_SECRET LOTUS_KEK; do
  if [[ -z "${!required:-}" ]]; then
    echo "❌ ${required} is not set. It must come from the environment, never" >&2
    echo "   a command-line flag — flags land in shell history and are visible" >&2
    echo "   in ps(1) to every user on the machine." >&2
    exit 1
  fi
done

log() { echo "==> $*"; }

log "Building CSS..."
command -v tailwindcss >/dev/null && \
  tailwindcss -i web/static/css/app.css -o web/static/css/app.built.css --minify

log "Building linux/amd64 binary..."
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/lotus ./cmd/lotus
shasum -a 256 bin/lotus | awk '{print $1"  bin/lotus"}' > bin/lotus.sha256
ls -la bin/lotus

log "Rendering runtime env..."
umask 077
tmp_env="$(mktemp)"
trap 'rm -f "${tmp_env}"' EXIT
cat > "${tmp_env}" <<EOF
ADDR=${APP_ADDR}
LOTUS_DEV=0
COOKIE_SECURE=1
LOTUS_MODE=${LOTUS_MODE:-single}
LOTUS_URL=${LOTUS_URL:-https://${APP_DOMAIN}}
LOTUS_BASE_DOMAIN=${LOTUS_BASE_DOMAIN:-${APP_DOMAIN}}
DATABASE_URL=${DATABASE_URL}
MIGRATE_DATABASE_URL=${MIGRATE_DATABASE_URL}
SESSION_SECRET=${SESSION_SECRET}
LOTUS_KEK=${LOTUS_KEK}
LOTUS_WORKER_COUNT=${LOTUS_WORKER_COUNT:-4}
GITHUB_CLIENT_ID=${GITHUB_CLIENT_ID:-}
GITHUB_CLIENT_SECRET=${GITHUB_CLIENT_SECRET:-}
GOOGLE_CLIENT_ID=${GOOGLE_CLIENT_ID:-}
GOOGLE_CLIENT_SECRET=${GOOGLE_CLIENT_SECRET:-}
SMTP_HOST=${SMTP_HOST:-}
SMTP_PORT=${SMTP_PORT:-587}
SMTP_USER=${SMTP_USER:-}
SMTP_PASSWORD=${SMTP_PASSWORD:-}
SMTP_FROM=${SMTP_FROM:-Lotus <no-reply@javan.co.id>}
EOF

log "Uploading..."
scp -q bin/lotus "${REMOTE}:/tmp/lotus.new"
scp -q bin/lotus.sha256 "${REMOTE}:/tmp/lotus.new.sha256"
scp -q "${tmp_env}" "${REMOTE}:/tmp/lotuscheck.env"
scp -q deploy/update.sh "${REMOTE}:/tmp/update.sh"

log "Installing env + running update.sh on server..."
ssh "${REMOTE}" bash <<'ENDSSH'
  set -e
  sudo install -d -m 0750 -o root -g lotuscheck /etc/lotuscheck
  sudo install -o root -g lotuscheck -m 0640 /tmp/lotuscheck.env /etc/lotuscheck/lotuscheck.env
  rm -f /tmp/lotuscheck.env
  sudo bash /tmp/update.sh /tmp/lotus.new
  rm -f /tmp/update.sh
ENDSSH

log "🎉 Deployed to https://${APP_DOMAIN}"
