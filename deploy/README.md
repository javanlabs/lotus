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

### Unit and nginx changes reach a server only by hand

`deploy.sh` ships the binary and the env; the systemd unit and the nginx
site are installed once, by `bootstrap.sh`. Re-running `bootstrap.sh` on a
live server overwrites the certbot-managed TLS blocks, so a change to
`lotuscheck-web.service` or `nginx-lotuscheck.conf` is applied in place:
back the file up, make the same edit, `systemd-analyze verify` /
`nginx -t`, then `daemon-reload` / `reload nginx`.

Uploaded files (QA attachments) live in `/var/lib/lotuscheck/blob`
(`StateDirectory=lotuscheck` in the unit, `LOTUS_BLOB_DIR` in the rendered
env). Back it up with the database. If Lotus cannot create it, it starts
anyway and logs `blob store … unavailable`; file uploads and downloads
answer 503 until it is fixed.

Applied on collab on 2026-09-27: `StateDirectory=lotuscheck` and
`StateDirectoryMode=0750` in the unit; nginx `client_max_body_size 4m` for
the site and a `25m` location for the QA upload routes. Backups:
`/etc/systemd/system/lotuscheck-web.service.bak-20260927` and
`/root/nginx-lotuscheck.bak-20260927`.

Repository scans (2026-10-17) need, on a server provisioned before them:
`sudo apt-get install git` and trivy from Aqua's apt repository (the
commands are in `bootstrap.sh`), then in the unit `CacheDirectory=lotuscheck`,
`CacheDirectoryMode=0750` and
`Environment=TRIVY_CACHE_DIR=/var/cache/lotuscheck/trivy`, then
`daemon-reload` and a restart. Until trivy is there Lotus starts as before
and logs `trivy repository scans off: …`. The first scan downloads Trivy's
vulnerability database (~1.5 GB unpacked) into that cache, so leave room for
it and let the server reach `mirror.gcr.io` / `ghcr.io`.

Applied on collab on 2026-09-28, for the legacy Lotus migration:
`ALTER ROLE lotuscheck_migrate BYPASSRLS` — the retention and timeseries
passes run as that role across organizations, and under FORCE ROW LEVEL
SECURITY they had been reading every tenant table as empty (no rollups,
no raw-detail aging) since the first deploy; `bootstrap.sh` now grants it,
and both passes log an error at startup if their role cannot see tenant
rows. Organization `retention_days` set to 365. Partitions
`*_2026_06` and `metric_facts_2026_07/08` created by hand as
`lotuscheck_migrate` (the importer now makes the next month's itself).

## Migrating from the legacy Lotus (lotus.alurkerja.com)

Done on 2026-09-28; kept for reference. On collab, in order:

1. Settings → Organization → Snapshot retention ≥ the history's age
   (365 days); `import-legacy` refuses otherwise.
2. `lotus seed-connections` with `/etc/lotuscheck/lotuscheck.env` and
   `/etc/lotus/lotus.env` loaded and `LOTUS_DEV=1` for that one command —
   the tokens never leave the server.
3. `lotus seed-services --file=/opt/lotus/services.yaml --only=alurkerja --org=javan`.
4. `sudo python3 deploy/legacy-lotus-export.py /opt/lotus/data/lotus.db > /root/legacy-import/legacy.jsonl`
   (read-only; the metric tables only).
5. `lotus import-legacy --org=javan --file=… --dry-run`, then without
   `--dry-run`, as a `systemd-run` unit with the env file. Rerunning it
   resumes, and refills every month's rollups.

## The KEK

`LOTUS_KEK` wraps every organization's data-encryption key, which in turn
seals every stored integration credential. Losing it makes every stored
credential permanently unreadable; changing it by hand does the same. It
lives in the masked GitLab variable and nowhere else. Rotate only with:

```bash
lotus rewrap-keys --old-kek=<previous base64>   # new key read from LOTUS_KEK
```
