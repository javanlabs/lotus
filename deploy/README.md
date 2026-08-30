# Deploying Lotus

Production runs one static binary behind nginx, with Postgres on the same
host. There is no container, no git checkout on the server, and no
hand-edited configuration: `/etc/lotuscheck/lotuscheck.env` is rendered
from GitLab CI/CD variables on every deploy, which makes GitLab the single
source of truth for production config.

| | |
|---|---|
| URL | <https://lotus.javan.id> |
| Server | `ssh collab` (15.235.197.61, Ubuntu 24.04) |
| Mode | `LOTUS_MODE=single` — one organization, one origin |
| App port | `127.0.0.1:8085` |
| Service | `lotuscheck-web.service` |
| Binary | `/opt/lotuscheck/bin/lotus` (previous kept as `lotus.previous`) |
| Config | `/etc/lotuscheck/lotuscheck.env`, `0640 root:lotuscheck` |
| Database | Postgres 16, database `lotuscheck` |
| nginx | `/etc/nginx/sites-available/lotuscheck` |
| TLS | Let's Encrypt on the origin; Cloudflare proxies the edge |

## Everything is namespaced `lotuscheck`, not `lotus`

An unrelated application — the AlurKerja on-premise toolkit, serving
`lotus.alurkerja.com` from `gitlab.javan.co.id/alurkerja/on-premises/toolkits/lotus`
— already owns the `lotus` UNIX user, `/opt/lotus`, `/etc/lotus`,
`lotus-web.service`, the `lotus` nginx site and port 8080 on this host.
Nothing here may reuse those names. The product is still called Lotus; only
its OS-level identity is `lotuscheck`, matching the repository name.

Ports on this host are crowded: 8080 that toolkit, 8081 ceo-monitor, 8082
talisa, 8083 alurkerja-dev, 8084 docker. Lotus took 8085.

## Files

| File | Runs where | Purpose |
|---|---|---|
| `bootstrap.sh` | your laptop | one-time provisioning over SSH |
| `deploy.sh` | your laptop | build + deploy without CI |
| `update.sh` | the server | migrate, swap binary, health-check, roll back |
| `nginx-lotuscheck.conf` | server | vhost, installed by `bootstrap.sh` |
| `lotuscheck-web.service` | server | systemd unit, installed by `bootstrap.sh` |
| `lotuscheck.env.example` | — | documented shape of the rendered env file |

## First-time provisioning

Already done for `lotus.javan.id`. To stand up another host:

```bash
PROD_SERVER_HOST=collab DOMAIN=lotus.javan.id ./deploy/bootstrap.sh
```

It creates the `lotuscheck` user and directories, provisions Postgres, and
installs the systemd unit and nginx site. It is idempotent — existing roles
keep their passwords — and it prints the two database DSNs **once**. Save
them into GitLab CI/CD variables immediately; they are stored nowhere else.

### Postgres roles

Two login roles mirror the DDL/DML split the schema is built around:

- `lotuscheck_migrate` owns the schema and runs `lotus migrate`.
- `lotuscheck_app` is the app's runtime role: DML only, and deliberately
  **not** a superuser. This is not cosmetic. `FORCE ROW LEVEL SECURITY` is
  bypassed unconditionally for superusers, so connecting the app as one
  would leave every tenant-isolation policy silently inert while every test
  still passed.

Both are members of the `lotus_app` / `lotus_migrate` group roles that
migration `00003` creates, which is what lets `internal/db.OpenApp`'s
`SET ROLE lotus_app` succeed.

## Deploying

Push to `main`, then run the manual `deploy_prod` job. Or from your laptop,
reading the secrets back out of GitLab so they exist in exactly one place:

```bash
R=internal-growth/qa-tools/lotuscheck
export SESSION_SECRET="$(glab variable get SESSION_SECRET -R $R)"
export LOTUS_KEK="$(glab variable get LOTUS_KEK -R $R)"
export DATABASE_URL="$(glab variable get DATABASE_URL -R $R)"
export MIGRATE_DATABASE_URL="$(glab variable get MIGRATE_DATABASE_URL -R $R)"
PROD_SERVER_HOST=collab ./deploy/deploy.sh
```

Credentials go through the environment, never command-line flags: a flag
lands in shell history and is visible in `ps(1)` to every user on the box.

`deploy_prod` is `when: manual` on purpose. A green pipeline is not by
itself a reason to restart a live instance under whoever is reading the
dashboard.

### What `update.sh` does

1. Verifies the binary's sha256 against its sidecar.
2. Runs `lotus migrate up` **with the old binary still serving**, so a bad
   migration aborts the deploy instead of stranding a new binary on an old
   schema. Lotus never migrates at boot — with more than one replica that
   races, and the app role has no DDL rights anyway.
3. Swaps the binary via `install(1)` (write-to-temp-then-rename, so no
   request sees a half-written file), keeping `.previous`.
4. Restarts the unit and polls `/healthz` for 30s; on failure it restores
   `.previous`, restarts, and prints the last 80 journal lines.

Rollback is not automatic for the schema: a migration that already applied
stays applied. Migrations must be backward-compatible with the previous
binary for the rollback path to be worth anything.

## TLS and Cloudflare

`javan.id` is on Cloudflare and `lotus.javan.id` is proxied (orange cloud),
so there are two hops of TLS: browser→Cloudflare uses Cloudflare's
`*.javan.id` edge certificate, and Cloudflare→origin uses the Let's Encrypt
certificate on this server.

The origin certificate is not optional. Until it existed, Cloudflare
answered **526 Invalid SSL certificate** for every request. Issue or
reissue it with:

```bash
ssh collab 'sudo certbot --nginx -d lotus.javan.id --redirect'
```

HTTP-01 works through the proxy — Cloudflare passes
`/.well-known/acme-challenge/` to the origin. Renewal is on certbot's own
systemd timer.

Client IPs arrive as Cloudflare's, not the visitor's. Nothing in Lotus
depends on the client IP today; if per-IP rate limiting lands, add
`set_real_ip_from` for Cloudflare's ranges plus
`real_ip_header CF-Connecting-IP` to the vhost, or every caller will look
like one address.

## Switching to multi-tenant (saas) mode

This instance is `single` because saas needs two things that do not exist
yet, neither of which is a code change:

1. **Wildcard DNS.** `*.lotus.javan.id` does not resolve. Every tenant is
   `<slug>.lotus.javan.id` and the auth host is `auth.lotus.javan.id`.
2. **A wildcard certificate.** Those names are two labels below `javan.id`,
   and Cloudflare's edge certificate is `*.javan.id` — one level only, so it
   does not cover them. That needs either Cloudflare Advanced Certificate
   Manager (paid) for `*.lotus.javan.id` at the edge, or a grey-clouded
   wildcard record plus a Let's Encrypt wildcard on the origin. The latter
   is DNS-01 only, so it needs `certbot-dns-cloudflare` (not installed on
   this host — only `nginx`, `standalone` and `webroot` are) and a
   Cloudflare API token scoped to the zone.

Once both exist, the switch is configuration:

```bash
glab variable set LOTUS_MODE -R internal-growth/qa-tools/lotuscheck <<< saas
glab variable set LOTUS_BASE_DOMAIN -R internal-growth/qa-tools/lotuscheck <<< lotus.javan.id
```

plus widening `server_name` in the vhost to `lotus.javan.id
*.lotus.javan.id` and pointing it at the wildcard certificate. `LOTUS_URL`
is ignored in saas mode. Note that a saas instance serves organizations by
subdomain, so the single organization created by `lotus init` would need a
slug that matches its intended host.

## Logging in

Set at least one of these before inviting anyone, or **no one can log in**:

- `SMTP_HOST` and friends — magic-link login emails the link, and outside
  `LOTUS_DEV=1` the link exists nowhere else. Not even the journal has it.
- `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET`, or the `GOOGLE_*` pair.
  Redirect URI in single mode is
  `https://lotus.javan.id/login/<github|google>/callback`.

## Operating

```bash
ssh collab 'sudo systemctl status lotuscheck-web'
ssh collab 'sudo journalctl -u lotuscheck-web -f'
ssh collab 'sudo systemctl restart lotuscheck-web'
```

Roll back by hand:

```bash
ssh collab 'sudo install -o lotuscheck -g lotuscheck -m 0755 \
  /opt/lotuscheck/bin/lotus.previous /opt/lotuscheck/bin/lotus && \
  sudo systemctl restart lotuscheck-web'
```

## The KEK

`LOTUS_KEK` wraps every organization's data-encryption key, which in turn
seals every stored integration credential. Losing it makes every stored
credential permanently unreadable; changing it by hand does the same. It
lives in the masked GitLab variable and nowhere else. Rotate only with:

```bash
lotus rewrap-keys --old-kek=<previous base64>   # new key read from LOTUS_KEK
```
