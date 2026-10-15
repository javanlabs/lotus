# Lotus

Multi-tenant SaaS engineering-metrics dashboard (GitLab, SonarQube,
Trivy/Harbor, Sentry) — the productized successor to the internal `lotus`
tool. Built with a deliberately lightweight stack: **Go** + **Fiber v2**
(server-rendered `html/template`), **htmx**, **Alpine.js**, **Tailwind CSS
4**, and **PostgreSQL**.

Full spec: [ActiveCollab project 697](https://collab.javan.co.id/projects/697)
("Lotus SaaS"). This repo currently implements **Fase 0 — Fondasi
Multi-tenant**: repo scaffold, the control-plane and tenant-plane schemas,
Row-Level Security + the composite-FK anti-drift pattern, the Go
`tenant.Scope` guardrail, and the subdomain resolver. Auth, RBAC,
integrations, and the real UI land in the phases after this one — see the
ActiveCollab project for the full roadmap.

## Run

Requires Go 1.26+ and a local Postgres reachable on `:5418` (either an
existing server — e.g. [DBngin](https://dbngin.com)/Herd — or Docker, which
`make db-up` falls back to automatically). The Makefile downloads the
Tailwind standalone binary on first use (no Node needed for the app itself
— Node is only used by the E2E suite, see below).

```sh
make db-up       # start/verify local Postgres on :5418 (lotus, lotus_test)
make migrate-up  # apply schema migrations
make run-once    # one-shot dev server on :8080 (LOTUS_DEV=1, no hot reload)
```

Open http://localhost:8080. `make dev` runs the same thing with hot reload
(air rebuilds Go changes, Tailwind watches CSS).

```sh
make test        # go test ./... against a real (per-test-schema) Postgres
make lint        # go vet + the task-0.4 repo-scope guardrail script
```

### E2E tests (Playwright)

```sh
cd e2e && npm install && npx playwright install chromium
npx playwright test
```

Playwright boots its own server instance (`make css` + `go run ./cmd/lotus
serve`) against whatever `DATABASE_URL` / local Postgres is already up —
run `make db-up` first if you haven't already.

## Environment variables

None of these need to be set for local dev — every one has a working
default. Copy `.env.example` to `.env` to override any of them (skipped
entirely when `LOTUS_DEV=0`, e.g. in production, where systemd's
EnvironmentFile is the only source of config — see `deploy/`, added in task
5.2).

| Variable | Default | Purpose |
|---|---|---|
| `ADDR` | `:8080` | Address the web process listens on. |
| `DATABASE_URL` | `postgres://postgres@localhost:5418/lotus?sslmode=disable` | App runtime DSN — connects as the RLS-restricted `lotus_app` role in production. |
| `MIGRATE_DATABASE_URL` | same as `DATABASE_URL` | DSN used only by `lotus migrate` — connects as the DDL-capable `lotus_migrate` role in production. Falls back to `DATABASE_URL` for local dev, where one role is fine. |
| `LOTUS_BLOB_DIR` | `var/blob` | Directory holding uploaded files (QA attachments). Production must point it at persistent, backed-up disk. |
| `LOTUS_DEV` | unset (off) | `1` enables dev mode: templates/static assets are read from disk (hot reload) instead of the embedded build, and the `X-Lotus-Org` header fallback (below) is honored. |
| `SESSION_SECRET` | random per-boot | Signs session cookies. Set to a stable value in production, or sessions reset on every restart. |
| `LOTUS_MODE` | `saas` | `saas` = multi-tenant with subdomains (apex / auth host / `<slug>.<domain>`). `single` = one organization on one origin, for self-hosted and local installs: no subdomains, so no wildcard DNS and no wildcard certificate. |
| `LOTUS_URL` | unset | Absolute base URL of a `single`-mode install, e.g. `http://localhost:8008`. **Required when `LOTUS_MODE=single`** (startup fails without it) and unused in `saas` mode. It replaces `LOTUS_BASE_DOMAIN` as what absolute links — OAuth redirect URIs, invitation links — are built against. |
| `LOTUS_BASE_DOMAIN` | `lotus.localhost` | Wildcard root Lotus is served under. Required in `saas` mode, **ignored when `LOTUS_MODE=single`** — apex is this value, auth host is `auth.`+this, tenants are `<slug>.`+this. Production is `lotus.javan.id`, but the deployed instance runs `single` mode, where this is ignored — see [`deploy/README.md`](deploy/README.md). |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | unset | GitHub OAuth app credentials (task 1.1). Login via GitHub is simply not offered when the secret is empty. Redirect URI to register: `<scheme>://auth.<LOTUS_BASE_DOMAIN>/login/github/callback`. |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | unset | Google OAuth app credentials (task 1.1). Same on/off behavior as GitHub's. Redirect URI: `<scheme>://auth.<LOTUS_BASE_DOMAIN>/login/google/callback`. |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USER` / `SMTP_PASSWORD` / `SMTP_FROM` | unset (`SMTP_PORT` defaults `587`) | Sends invitation emails (task 1.7). With `SMTP_HOST` empty, no mail is sent — invitations still work via the copyable link. |
| `OIDC_ISSUER` | unset | Issuer URL of any OpenID Connect provider, e.g. `https://sso.javan.id/realms/internal`. Endpoints are read from its `/.well-known/openid-configuration`, so nothing else needs configuring. Required when `OIDC_CLIENT_SECRET` is set — startup fails otherwise, rather than leaving a login button that only ever errors. |
| `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | unset | Credentials for that issuer. Registered as the `sso` provider, so the redirect URI to whitelist is `<base>/login/sso/callback`. Like the GitHub and Google pairs, an empty secret simply means the provider is not registered. |
| `OIDC_TRUST_EMAIL` | unset (off) | `1` treats the OIDC issuer's email addresses as verified even when its `email_verified` claim is false. A verified address is what links accounts, accepts invitations and claims a fresh single-mode instance's Owner seat, so only enable it for an IdP whose users cannot choose their own address — an administrator-provisioned corporate Keycloak, not a public sign-up realm. Javan's Keycloak needs it: it never marks addresses verified. |
| `LOTUS_KEK` | dev-only fixed value (see warning below) | Base64, 32-byte key-encryption-key that wraps each organization's per-org DEK (task 2.2). Every integration credential is sealed under its org's DEK, AES-256-GCM, with the org's id as AAD — a credential row copied to another organization fails to decrypt even with the right key. **The dev default is checked into source control (`internal/config.devOnlyKEK`) — production must always set a real `LOTUS_KEK`, or every credential is encrypted under a key anyone with this repo already has.** Rotate with `lotus rewrap-keys --old-kek=<base64>` (new key read from `LOTUS_KEK`) — re-wraps every org's DEK without touching a single credential row, so reads never see downtime. |
| `LOTUS_FEATURE_QA` | off | `1` or `true` turns on QA Tools — test cases, scenarios, plans, runs, issues, project guests, the QA JSON API and its MCP server. Off by default: its routes are not registered at all, so every QA URL answers 404 and nothing else in the app links to it. Any other value, including a typo, leaves it off. |
| `LOTUS_WORKER_COUNT` | `4` | Sync worker pool size (task 3.2) — goroutines within the `serve` process claiming and executing queued `sync_runs`, not separate OS processes. |
| `COOKIE_SECURE` | `!LOTUS_DEV` | Forces the `Secure` cookie flag on/off. Defaults to on outside dev mode. |
| `X-Lotus-Org` (header, not env) | — | Dev-only fallback so `curl`/tests can hit a tenant without wildcard DNS or a custom Host header. Only honored when `LOTUS_DEV=1`. |

Test-only (set by `make test` / CI, never needed by hand):

| Variable | Purpose |
|---|---|
| `TEST_DATABASE_URL` | Postgres DSN Go tests create per-test schemas against (see `internal/dbtest`). Tests `t.Skip()` when unset. |
| `LOTUS_E2E_PORT` / `LOTUS_E2E_BASE_URL` | Override the port/URL Playwright's `webServer` boots against. Default port `8095`. |

## Architecture notes

- **Two-plane multi-tenancy.** Control plane (`users`, `organizations`,
  `memberships`, `sessions`, ...) has no per-org Row-Level Security — it's
  read before Lotus knows which organization is speaking. Every
  tenant-plane table (`projects`, `components`, ...) carries `org_id NOT
  NULL`, has RLS **FORCE**d on, and is linked to its parent by a composite
  foreign key `(parent_id, org_id) REFERENCES parent(id, org_id)` so a
  child row's `org_id` can't drift from its parent's — Postgres rejects the
  insert outright.
- **Defense in depth, each layer independently necessary.** RLS
  (`internal/db/migrations/00003_tenant_plane.sql`) is the fail-safe net;
  `tenant.Scope`'s unexported constructor (`internal/tenant/scope.go`) is
  the layer that keeps it rarely touched — a handler has no way to open a
  query that isn't already bound to an organization; `scripts/check-repo-
  scope.sh` (wired into `make lint` and CI) catches a raw `db.*` call
  outside `internal/repo` before it ships.
- **`tenant.Resolve()` is Layer 0.** The subdomain resolver
  (`internal/tenant/resolve.go`) turns the Host header into an
  organization (or a flat 404 — it never falls back to any organization)
  before anything else runs. Reserved and retired subdomains live in DB
  tables, not Go constants, so ops can add one without a deploy.
- **Migrations run only via `lotus migrate`, never at boot** — with
  multiple replicas, running DDL at process start means processes race on
  schema changes; the app's own DB role has no DDL rights at all.
- **The app's runtime connection always `SET ROLE lotus_app`** (`internal/db.OpenApp`,
  used by `cmd/lotus serve` and by `internal/dbtest` for every Go test) —
  never the raw DSN role directly. `DATABASE_URL`/`TEST_DATABASE_URL` connect
  as the `postgres` superuser in local dev for convenience, and a superuser
  session bypasses Row-Level Security unconditionally, `FORCE ROW LEVEL
  SECURITY` notwithstanding. Without this, RLS (the tenant-isolation
  fail-safe net described above) would silently do nothing in dev and in
  every test — found while building task 2.1, logged to ActiveCollab.
  `db.Open` (no role switch) is still used for `lotus migrate`, which needs
  its real DDL rights.
- **`html/template` + htmx + Alpine + Tailwind v4, no component framework.**
  Assets are `//go:embed`ed for production and read from disk in
  `LOTUS_DEV=1` mode (see `web/assets.go`, `internal/httpx/render.go`).
- **Every outbound fetch of a user-supplied URL goes through `internal/netguard`**
  (task 2.2). `Connection.BaseURL` is attacker-reachable input this server
  itself later fetches while carrying a credential — `netguard.NewClient`
  refuses to connect to private/loopback/link-local/cloud-metadata
  addresses, re-validating on every redirect hop, and validates the
  *actually-dialed* IP (not a separately-resolved one) specifically to
  close a DNS-rebinding gap between check-time and connect-time.
- **`sync_runs` and `connection_sync_state` are the only tenant-plane-shaped tables with no RLS** (task 3.2). Both carry `org_id`, but the scheduler's claim query is inherently cross-tenant — it fairly picks the next runnable job across *every* organization in one query, which is structurally incompatible with being bound to a single `app.org_id`. Verified directly against Postgres before shipping: with `app.org_id` unset (required for a query to see every org), RLS hides 100% of an RLS'd table's rows, for any org, not a subset — a first attempt that joined the claim query against the (RLS'd) `integration_connections` table returned zero claimable runs, always, which is why the rate-limit/backoff state was pulled out into `connection_sync_state`, a dedicated unscoped sidecar holding only that operational state. The moment a worker has one specific run in hand and goes to write its result, that write goes through `tenant.FromRun(db, run.OrgID)` like any other tenant-plane write, RLS and all.
- **Credentials are write-only by construction, not by convention.**
  `repo.Connection` (task 2.2) has no ciphertext, nonce, or plaintext
  field at all — there is no struct a handler could accidentally
  serialize into a response even by mistake. The only function that ever
  produces plaintext, `repo.DecryptConnectionSecret`, is called from
  exactly one place (`internal/web/connections.go`'s Test Connection
  handler), and the plaintext never leaves that function's stack.

## Layout

```
cmd/lotus                serve | migrate | rewrap-keys (task 2.2) | import-legacy (task 5.1, not yet implemented)
internal/config          env / .env loading
internal/db               Postgres connection + goose migrations (embedded)
internal/db/migrations    schema (goose SQL migrations)
internal/dbtest           per-test isolated-schema Postgres harness
internal/tenant           subdomain resolver + Scope type (task 0.4/0.5)
internal/repo              tenant-scoped queries only (task 0.4) — Project & Component (2.1), Connection & audit (2.2), Metric binding (2.3), Discovery (2.4)
internal/repo/global       the one exception: control-plane queries (no org scope) — per-org DEK storage (2.2), the sync queue + connection rate-limit state (3.2, deliberately unscoped — see below)
internal/repo/legacyqa     read-only reader for the legacy QA Tools database (QA import)
internal/qaimport          `lotus import-qa`: legacy QA Tools → one organization, plus `verify`
internal/blob              blob store for uploaded files (QA attachments)
internal/collector          Collector/Session/Ingestor interface, Result/Fact/Headline, error class taxonomy, registry + persister registry (task 3.1)
internal/collector/gitlab    GitLab Collector: FetchHealth → Headline/Facts/Warnings, gitlab_snapshots persistence (task 3.3)
internal/collector/sonarqube SonarQube Collector: measures → Headline/Facts, sonar_snapshots persistence (task 3.3)
internal/clients/gitlab       GitLab REST v4 client, ported from the legacy Lotus as-is (task 3.3)
internal/clients/sonar         SonarQube client, ported from the legacy Lotus as-is (task 3.3)
internal/scheduler           sync queue orchestration: enqueuer, worker pool, leader election, rate limiting, backoff (task 3.2)
internal/reqctx            the one piece of per-request state internal/authn and internal/authz both need (breaks their import cycle)
internal/authz             RBAC: Principal, Require/Public route markers, permission matrix (task 1.4/1.5)
internal/authn             OAuth (1.1), handoff (1.2), sessions (1.3), onboarding (1.6), invitations (1.7), API tokens (1.8)
internal/mailer             SMTP sender (task 1.7), no-op when unconfigured
internal/crypto             envelope encryption: KEK/DEK, AES-256-GCM with org-id AAD (task 2.2)
internal/netguard            SSRF-guarded http.Client for fetching user-supplied URLs (task 2.2)
internal/integrations        Descriptor/ValidateConfig (2.3), Discover per provider (2.4), + minimal per-provider live checks for Test Connection (2.2) and Test Binding (2.3); full collectors are Fase 3
internal/httpx             html/template renderer (layout + page + htmx partial)
internal/web                HTTP routes + task 1.5's route-coverage test
web/templates, web/static  layout, pages, vendored htmx/Alpine, Tailwind source
e2e                         Playwright end-to-end tests
scripts                     CI guardrail scripts
deploy                      production deploy (bootstrap/deploy/update scripts, nginx, systemd) + local dev docker-compose
```

### QA import

```sh
lotus import-qa --legacy-dsn='postgres://ro@host/qa_tools_dbs' --org=javan \
  --users-map=users.csv --files-dir=/srv/qa-minio-mirror --dry-run --report=dry.json
lotus import-qa verify --legacy-dsn='…' --org=javan
```

Full procedure: `docs/qa-import-runbook.md`.

## Production

Lotus runs at **<https://lotus.javan.id>**, single mode, on the `collab`
host — see [`deploy/README.md`](deploy/README.md) for the full picture.
Deploys are the manual `deploy_prod` job on `main`, or `deploy/deploy.sh`
from a laptop; runtime config is rendered from GitLab CI/CD variables on
every deploy, so nothing on the server is hand-edited.

It runs `LOTUS_MODE=single` rather than the multi-tenant default because
saas needs a wildcard DNS record and a certificate valid two labels below
`javan.id`, and Cloudflare's edge certificate is `*.javan.id` — one level
only. Neither is a code change; both are described under "Switching to
multi-tenant (saas) mode" in the deploy README.

## Status / open items

Tracked as tasks in [ActiveCollab project 697](https://collab.javan.co.id/projects/697):
the saas-mode wildcard DNS and certificate above, an SMTP or OAuth
credential without which no one can log in to the deployed instance, and
anything else flagged along the way.

## Single-tenant / self-hosted

Lotus installs two ways off the same binary. `LOTUS_MODE=saas` (the default)
is the multi-tenant product: wildcard DNS, an auth host, one organization per
subdomain. `LOTUS_MODE=single` is one organization on one origin — no
subdomains, so an ordinary single-name certificate is enough and local dev
needs no DNS setup at all.

```bash
export LOTUS_MODE=single
export LOTUS_URL=http://localhost:8008
export ADDR=:8008

lotus migrate up
lotus init --org-name="Acme" --owner-email=admin@acme.com
lotus serve
```

### Logging in

With no OAuth provider configured, login is by **magic link**: enter an address
on `/login` and a single-use link is issued for it. The link expires in 15
minutes, works once, and is only ever created for an address that already holds
a membership — a stranger's address gets the same "check your email" response
but no link, so the form cannot be used to discover who has an account.

Under `LOTUS_DEV=1` the link is also printed on the page, since local dev
usually has no SMTP to receive it. Outside dev it exists only in the email.

For repeated testing, seed four accounts — one per role:

```bash
lotus seed-dev
```

That creates `owner@lotus.test`, `admin@lotus.test`, `member@lotus.test`, and
`viewer@lotus.test`, and `/login` grows a one-click list to sign in as any of
them. That shortcut is gated three independent ways: the route is not
registered unless `LOTUS_DEV=1`, the handler re-checks the same flag, and it
will only ever impersonate an address ending in `@lotus.test` — a TLD reserved
by RFC 2606, so it can never name a real person. `lotus seed-dev` itself
refuses to run without `LOTUS_DEV=1`.

Sign in as the `--owner-email` address to claim ownership: `lotus init`
creates the organization but cannot create its Owner (nobody has logged in
yet), so ownership stays pending until the first login with that address,
**verified by the identity provider**. Until then no other account can sign
in, so a fresh instance is never a land-grab.

What single mode changes:

- The subdomain resolver skips host classification entirely — any `Host`
  resolves to the one organization, so a hostname, a bare IP, or a
  proxy-rewritten `Host` all work.
- The handoff (`/handoff/*`), self-serve signup (`/signup`), and onboarding
  (`/onboarding/*`) routes are **not registered**: they exist only because
  saas mode splits one product across three hosts and lets anyone create an
  organization. Members join by invitation instead.
- `/` serves the app rather than the apex placeholder.
- `X-Lotus-Org` is ignored — it selects between tenants, and there is one.

A single-mode install holds **exactly one** organization; `lotus init`
refuses to run twice, and the app refuses to start resolving if a second
organization ever appears.
